package pricing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"
)

const (
	rateInput = iota
	rateOutput
	rateCacheRead
	rateCacheWrite
	rateClasses
)

// rateFields names the read rate fields of a table entry, indexed by
// rate class.
var rateFields = [rateClasses]string{
	rateInput:      "input_cost_per_token",
	rateOutput:     "output_cost_per_token",
	rateCacheRead:  "cache_read_input_token_cost",
	rateCacheWrite: "cache_creation_input_token_cost",
}

const providerField = "litellm_provider"

// Table is a validated model price table. It never changes after
// ParseTable or EmbeddedTable returns it and is safe for concurrent use.
type Table struct {
	entries map[string]tableEntry
	index   nameIndex
	version string
	asOf    time.Time
}

// TableEntry is the entry a model name resolved to. A nil rate means the
// entry carries no rate for that class.
type TableEntry struct {
	Key                string   // the table key the name resolved to
	InputPerToken      *float64 // input_cost_per_token, USD per token
	OutputPerToken     *float64 // output_cost_per_token, USD per token
	CacheReadPerToken  *float64 // cache_read_input_token_cost, USD per token
	CacheWritePerToken *float64 // cache_creation_input_token_cost, USD per token
}

// tableEntry is an entry as the table stores it. Its pointers are never
// handed out, so a table stays immutable.
type tableEntry struct {
	provider string
	rates    [rateClasses]*float64
}

func (e tableEntry) hasRate() bool {
	for _, r := range e.rates {
		if r != nil {
			return true
		}
	}
	return false
}

// ParseTable validates data as a price table and returns it. A non-nil
// previous is the last table accepted from the source data came from, or the
// embedded table when data is the shared table and none has been accepted
// yet, never a table from another source: data must then carry rates in at
// least half as many entries, and an entry whose rate fell to zero keeps
// previous's entry.
func ParseTable(data []byte, previous *Table) (*Table, error) {
	entries, err := decodeEntries(data)
	if err != nil {
		return nil, err
	}

	rated := countRated(entries)
	if rated == 0 {
		return nil, errors.New("price table: no entry carries a rate")
	}
	if previous != nil {
		if prevRated := countRated(previous.entries); 2*rated < prevRated {
			return nil, fmt.Errorf("price table: %d entries carry a rate, fewer than half of the %d in the previous table", rated, prevRated)
		}
		keepPreviousOnZero(entries, previous.entries)
	}

	return &Table{
		entries: entries,
		index:   newNameIndex(entries),
		version: digest(entries),
	}, nil
}

// Version identifies the rates and providers in force: the pinned upstream
// revision for the embedded table, otherwise a content digest.
func (t *Table) Version() string {
	return t.version
}

// AsOf returns when the table's rates were taken, or the zero time when the
// table does not record it.
func (t *Table) AsOf() time.Time {
	return t.asOf
}

// Len returns the number of entries the table holds, the keys [Table.Lookup]
// can resolve to.
func (t *Table) Len() int {
	return len(t.entries)
}

// Encode returns the canonical encoding of the table. Parsing it again
// yields the same entries.
func (t *Table) Encode() []byte {
	return encode(t.entries)
}

// withRevision copies the table under a revision and date, because a
// parsed table is versioned by content and records no date.
func (t *Table) withRevision(version string, asOf time.Time) *Table {
	c := *t
	c.version = version
	c.asOf = asOf
	return &c
}

func decodeEntries(data []byte) (map[string]tableEntry, error) {
	// Unmarshal accepts a lone null as an empty object, so the opening
	// brace is checked first.
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("price table: data is not a JSON object")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("price table: %w", err)
	}

	entries := make(map[string]tableEntry, len(doc))
	for _, key := range slices.Sorted(maps.Keys(doc)) {
		if key == "" || key == "sample_spec" || key == "fallback_generalizations" {
			continue
		}
		raw := bytes.TrimSpace(doc[key])
		if raw[0] != '{' {
			continue
		}
		entry, err := decodeEntry(key, raw)
		if err != nil {
			return nil, err
		}
		entries[key] = entry
	}
	return entries, nil
}

func decodeEntry(key string, raw []byte) (tableEntry, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return tableEntry{}, fmt.Errorf("price table: entry %q: %w", key, err)
	}

	var entry tableEntry
	if p := bytes.TrimSpace(fields[providerField]); len(p) > 0 && string(p) != "null" {
		if p[0] != '"' {
			return tableEntry{}, fmt.Errorf("price table: entry %q: field %s is not a string", key, providerField)
		}
		if err := json.Unmarshal(p, &entry.provider); err != nil {
			return tableEntry{}, fmt.Errorf("price table: entry %q: field %s: %w", key, providerField, err)
		}
	}

	for class, field := range rateFields {
		v := bytes.TrimSpace(fields[field])
		if len(v) == 0 || string(v) == "null" {
			continue
		}
		rate, err := parseRate(v)
		if err != nil {
			return tableEntry{}, fmt.Errorf("price table: entry %q: field %s: %w", key, field, err)
		}
		entry.rates[class] = &rate
	}
	return entry, nil
}

func parseRate(v []byte) (float64, error) {
	if c := v[0]; c != '-' && (c < '0' || c > '9') {
		return 0, errors.New("not a number")
	}
	rate, err := strconv.ParseFloat(string(v), 64)
	if err != nil {
		return 0, err
	}
	if rate < 0 {
		return 0, errors.New("negative")
	}
	// "-0" parses to negative zero, which would encode differently from 0.
	if rate == 0 {
		rate = 0
	}
	return rate, nil
}

func countRated(entries map[string]tableEntry) int {
	n := 0
	for _, e := range entries {
		if e.hasRate() {
			n++
		}
	}
	return n
}

// keepPreviousOnZero replaces a candidate entry by the previous entry for
// its key when a rate fell from above zero to zero. A rate that is absent
// does not count: zero rates are real, and a missing rate is reported as
// missing downstream.
func keepPreviousOnZero(entries, previous map[string]tableEntry) {
	for key, candidate := range entries {
		old, ok := previous[key]
		if !ok {
			continue
		}
		for class := range rateClasses {
			o, c := old.rates[class], candidate.rates[class]
			if o != nil && *o > 0 && c != nil && *c == 0 {
				entries[key] = old
				break
			}
		}
	}
}

// encodedEntry fixes the member order of the canonical encoding.
type encodedEntry struct {
	Provider   string   `json:"litellm_provider,omitempty"`
	Input      *float64 `json:"input_cost_per_token,omitempty"`
	Output     *float64 `json:"output_cost_per_token,omitempty"`
	CacheRead  *float64 `json:"cache_read_input_token_cost,omitempty"`
	CacheWrite *float64 `json:"cache_creation_input_token_cost,omitempty"`
}

func encode(entries map[string]tableEntry) []byte {
	out := make(map[string]encodedEntry, len(entries))
	for key, e := range entries {
		out[key] = encodedEntry{
			Provider:   e.provider,
			Input:      e.rates[rateInput],
			Output:     e.rates[rateOutput],
			CacheRead:  e.rates[rateCacheRead],
			CacheWrite: e.rates[rateCacheWrite],
		}
	}
	// Strings and finite floats always encode.
	b, _ := json.MarshalIndent(out, "", "    ")
	return append(b, '\n')
}

func digest(entries map[string]tableEntry) string {
	sum := sha256.Sum256(encode(entries))
	return "sha256:" + hex.EncodeToString(sum[:])
}
