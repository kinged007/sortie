// Package pricing parses the token_rates workflow section and estimates the
// USD cost of token usage. [ParseTokenRates] and [EstimateCost] are the
// entry points for one entry; [TokenRateConfig.Complete] states which kind
// entries price anything. [LoadRates] puts the rate layers of one workflow
// load in force as [Rates], and [PriceRun] and [PriceSession] price a
// finished run and a running session under them.
//
// The package also holds the embedded price table ([EmbeddedTable]), the
// validation every price table passes ([ParseTable]), and the rule that
// maps a model name onto a table key ([Table.Lookup]).
package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/sortie-ai/sortie/internal/domain"
)

// rateKeys lists the four recognized token-rate keys, in the order
// ParseTokenRates checks them within one kind's entry.
var rateKeys = []string{"input_per_mtok", "output_per_mtok", "cache_read_per_mtok", "cache_write_per_mtok"}

// TokenRateConfig holds per-token-type USD rates for cost estimation.
// All rates are in USD per 1 million tokens (per-mtok). A nil pointer
// indicates the rate is not configured; cost estimation is suppressed
// for that token type.
type TokenRateConfig struct {
	InputPerMtok      *float64
	OutputPerMtok     *float64
	CacheReadPerMtok  *float64
	CacheWritePerMtok *float64
}

// Complete reports whether the entry sets both InputPerMtok and
// OutputPerMtok. An entry without both rates prices nothing.
func (c TokenRateConfig) Complete() bool {
	return c.InputPerMtok != nil && c.OutputPerMtok != nil
}

// TokenRates maps token_rates keys, agent adapter kinds or model names, to
// their token rate configuration. A nil or empty map holds no entry.
type TokenRates map[string]TokenRateConfig

// ParseTokenRates extracts and validates token rates from the raw
// "token_rates" extension value. present tells whether the key
// existed in the extensions map at all, distinguishing an absent key
// from one holding a nil value. Returns nil rates when the key is
// absent or empty. Warnings are advisory and do not prevent boot.
func ParseTokenRates(rawSection any, present bool) (TokenRates, []string) {
	if !present || rawSection == nil {
		return nil, nil
	}

	topMap, ok := rawSection.(map[string]any)
	if !ok {
		return nil, []string{fmt.Sprintf("token_rates: expected map, got %T", rawSection)}
	}
	if len(topMap) == 0 {
		return nil, nil
	}

	var warnings []string
	rates := make(TokenRates, len(topMap))

	for _, kind := range slices.Sorted(maps.Keys(topMap)) {
		val := topMap[kind]
		if kind == "" {
			warnings = append(warnings, "token_rates: entry keyed to the empty string is dropped")
			continue
		}

		cfg, entryWarnings, _ := readTokenRateEntry(kind, val)
		warnings = append(warnings, entryWarnings...)
		rates[kind] = cfg
	}

	if len(rates) == 0 {
		return nil, warnings
	}
	return rates, warnings
}

// readTokenRateEntry reads one raw entry for startup and reload alike.
// rejected reports an entry that is not a map or holds an invalid rate.
func readTokenRateEntry(kind string, raw any) (cfg TokenRateConfig, warnings []string, rejected bool) {
	kindMap, ok := raw.(map[string]any)
	if !ok {
		return cfg, []string{fmt.Sprintf("token_rates.%s: expected map, got %T", kind, raw)}, true
	}

	rateTargets := [...]**float64{&cfg.InputPerMtok, &cfg.OutputPerMtok, &cfg.CacheReadPerMtok, &cfg.CacheWritePerMtok}
	for i, key := range rateKeys {
		rate, warning := extractRate(kindMap, key, kind)
		if warning != "" {
			warnings = append(warnings, warning)
			rejected = true
			continue
		}
		*rateTargets[i] = rate
	}

	for _, key := range slices.Sorted(maps.Keys(kindMap)) {
		if slices.Contains(rateKeys, key) {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("token_rates.%s.%s: unrecognized key is ignored", kind, key))
	}
	return cfg, warnings, rejected
}

// reloadTokenRates returns the rates a reloaded token_rates section puts in
// force. A malformed section or entry keeps what inForce holds for it, so a
// bad reload never drops working rates. inForce is never mutated.
func reloadTokenRates(inForce TokenRates, rawSection any, present bool) TokenRates {
	if !present || rawSection == nil {
		return nil
	}
	topMap, ok := rawSection.(map[string]any)
	if !ok {
		return inForce
	}

	var result TokenRates
	for kind, raw := range topMap {
		if kind == "" {
			continue
		}
		entry, _, rejected := readTokenRateEntry(kind, raw)
		if kept, held := inForce[kind]; rejected && held {
			entry = kept
		}
		if result == nil {
			result = make(TokenRates, len(topMap))
		}
		result[kind] = entry
	}
	return result
}

// tokenRateDigestEntry fixes the member order of the canonical encoding
// that Version hashes.
type tokenRateDigestEntry struct {
	InputPerMtok      *float64 `json:"input_per_mtok"`
	OutputPerMtok     *float64 `json:"output_per_mtok"`
	CacheReadPerMtok  *float64 `json:"cache_read_per_mtok"`
	CacheWritePerMtok *float64 `json:"cache_write_per_mtok"`
}

// Version returns "sha256:" and the lowercase hex SHA-256 of the canonical
// JSON encoding of the rates, or "" for nil or empty rates. The encoding
// depends on the parsed values only, so comments, whitespace, key order, and
// number spelling in WORKFLOW.md never change it. Incomplete entries count.
func (r TokenRates) Version() string {
	if len(r) == 0 {
		return ""
	}
	entries := make(map[string]tokenRateDigestEntry, len(r))
	for kind, cfg := range r {
		entries[kind] = tokenRateDigestEntry(cfg)
	}
	// Rates passed extractRate, so they are finite and Marshal cannot fail.
	encoded, _ := json.Marshal(entries)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// extractRate reads a non-negative, finite float64 from a map entry.
// Returns nil with empty warning when the key is absent, nil with a
// warning when the value is invalid.
func extractRate(m map[string]any, key, kind string) (*float64, string) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil, ""
	}

	var f float64
	switch v := raw.(type) {
	case float64:
		f = v
	case int:
		f = float64(v)
	case int64:
		f = float64(v)
	default:
		return nil, fmt.Sprintf("token_rates.%s.%s: expected number, got %T", kind, key, raw)
	}

	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Sprintf("token_rates.%s.%s: rate %v is not finite", kind, key, f)
	}
	if f < 0 {
		return nil, fmt.Sprintf("token_rates.%s.%s: negative rate %v", kind, key, f)
	}
	return &f, ""
}

// EstimateCost prices usage under the disjoint-bucket rule: fresh
// input, cache reads, cache writes, and output are each priced once,
// at their own rate. An unset cache-read or cache-write rate prices
// that class at the input rate. Returns nil when rates is nil or
// lacks InputPerMtok or OutputPerMtok; it never reads usage.TotalTokens.
func EstimateCost(usage domain.TokenUsage, rates *TokenRateConfig) *float64 {
	if rates == nil || !rates.Complete() {
		return nil
	}

	readRate := *rates.InputPerMtok
	if rates.CacheReadPerMtok != nil {
		readRate = *rates.CacheReadPerMtok
	}
	writeRate := *rates.InputPerMtok
	if rates.CacheWritePerMtok != nil {
		writeRate = *rates.CacheWritePerMtok
	}

	fresh := max(usage.InputTokens-usage.CacheReadTokens-usage.CacheWriteTokens, 0)
	cost := (float64(fresh)*(*rates.InputPerMtok) +
		float64(usage.CacheReadTokens)*readRate +
		float64(usage.CacheWriteTokens)*writeRate +
		float64(usage.OutputTokens)*(*rates.OutputPerMtok)) / 1_000_000
	return &cost
}
