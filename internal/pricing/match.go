package pricing

import (
	"slices"
	"strings"
)

// providerAlias tells where the table lists the models of a provider that
// a runtime writes under another provider segment.
type providerAlias struct {
	segment  string // the provider segment a runtime writes, lowercase
	prefix   string // put before the rest of the name to form the candidate key; empty for none
	provider string // the litellm_provider the candidate's entry carries
}

// providerAliases holds catalog provider ids only, never agent kinds, so a
// name resolves alike whichever runtime reported it. A segment without a
// row stays unmatched: a routed or resold endpoint can cost many times the
// list price of the model it serves.
var providerAliases = []providerAlias{
	{segment: "google", prefix: "gemini/", provider: "gemini"},
	{segment: "google-vertex", provider: "vertex_ai-language-models"},
	{segment: "google-vertex", prefix: "vertex_ai/", provider: "vertex_ai-anthropic_models"},
	{segment: "google-vertex-anthropic", prefix: "vertex_ai/", provider: "vertex_ai-anthropic_models"},
	{segment: "amazon-bedrock", provider: "bedrock_converse"},
	{segment: "amazon-bedrock", provider: "bedrock"},
	{segment: "github-copilot", prefix: "github_copilot/", provider: "github_copilot"},
	{segment: "togetherai", prefix: "together_ai/", provider: "together_ai"},
	{segment: "fireworks-ai", prefix: "fireworks_ai/", provider: "fireworks_ai"},
	{segment: "novita-ai", prefix: "novita/", provider: "novita"},
	{segment: "vercel", prefix: "vercel_ai_gateway/", provider: "vercel_ai_gateway"},
	{segment: "cloudflare-workers-ai", prefix: "cloudflare/", provider: "cloudflare"},
	{segment: "moonshotai", prefix: "moonshot/", provider: "moonshot"},
}

// nameIndex resolves a name onto a table key by equality, or by equality of
// the folded forms when exactly one key has that form.
type nameIndex struct {
	exact  map[string]tableEntry
	folded map[string][]string
}

func newNameIndex(entries map[string]tableEntry) nameIndex {
	folded := make(map[string][]string, len(entries))
	for key := range entries {
		f := fold(key)
		folded[f] = append(folded[f], key)
	}
	return nameIndex{exact: entries, folded: folded}
}

// resolve returns the key name stands for, or false when none does or
// when two keys share its folded form.
func (ix nameIndex) resolve(name string) (string, bool) {
	if _, ok := ix.exact[name]; ok {
		return name, true
	}
	if keys := ix.folded[fold(name)]; len(keys) == 1 {
		return keys[0], true
	}
	return "", false
}

// fold keeps the length of s, so a folded form never matches by prefix or
// substring.
func fold(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case 'A' <= c && c <= 'Z':
			b[i] = c + ('a' - 'A')
		case c == '.' && i > 0 && i < len(b)-1 && isDigit(s[i-1]) && isDigit(s[i+1]):
			b[i] = '-'
		}
	}
	return string(b)
}

func isDigit(c byte) bool {
	return '0' <= c && c <= '9'
}

// equalFoldASCII compares letter case for ASCII only, matching fold.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// Lookup maps a model name onto an entry; false when no entry matches.
//
// A name maps to the key it equals, else to the one key it equals once
// ASCII letter case and a dot between two digits are set aside. Failing
// that, a name of the form provider/model maps to the one entry the table
// lists for model under that provider. An unmatched name, a placeholder in
// angle brackets, and a name two keys claim all return false; the result
// is the zero TableEntry. The rates are copies the table does not retain.
func (t *Table) Lookup(model string) (TableEntry, bool) {
	if model == "" || isPlaceholder(model) {
		return TableEntry{}, false
	}
	key, ok := t.index.resolve(model)
	if !ok {
		key, ok = t.resolveProviderQualified(model)
	}
	if !ok {
		return TableEntry{}, false
	}
	return t.entry(key), true
}

func isPlaceholder(name string) bool {
	return len(name) >= 2 && name[0] == '<' && name[len(name)-1] == '>'
}

func (t *Table) resolveProviderQualified(name string) (string, bool) {
	segment, rest, ok := strings.Cut(name, "/")
	if !ok || segment == "" || rest == "" {
		return "", false
	}

	tries := []providerAlias{{provider: segment}}
	for _, alias := range providerAliases {
		if equalFoldASCII(alias.segment, segment) {
			tries = append(tries, alias)
		}
	}

	var found []string
	for _, try := range tries {
		key, ok := t.index.resolve(try.prefix + rest)
		if !ok || !equalFoldASCII(t.entries[key].provider, try.provider) {
			continue
		}
		if !slices.Contains(found, key) {
			found = append(found, key)
		}
	}
	if len(found) != 1 {
		return "", false
	}
	return found[0], true
}

// entry copies the rates, so a caller cannot change the table.
func (t *Table) entry(key string) TableEntry {
	stored := t.entries[key]
	copyRate := func(class int) *float64 {
		if stored.rates[class] == nil {
			return nil
		}
		v := *stored.rates[class]
		return &v
	}
	return TableEntry{
		Key:                key,
		InputPerToken:      copyRate(rateInput),
		OutputPerToken:     copyRate(rateOutput),
		CacheReadPerToken:  copyRate(rateCacheRead),
		CacheWritePerToken: copyRate(rateCacheWrite),
	}
}
