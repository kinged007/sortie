package githubpr

import (
	"slices"
	"strconv"
	"strings"

	"github.com/sortie-ai/sortie/internal/httpkit"
)

// defaultEndpoint is the public GitHub REST API base URL, applied when
// the adapter config omits "endpoint".
const defaultEndpoint = "https://api.github.com"

func resolveEndpoint(raw string) (endpoint, redacted string, ok bool) {
	parsed, ok := httpkit.ResolveEndpoint(raw, defaultEndpoint)
	if !ok {
		return "", parsed.Redacted, false
	}
	return parsed.Base, parsed.Redacted, true
}

func containsSlash(s string) bool {
	return strings.Contains(s, "/")
}

func lowerStates(raw []string, fallback []string) []string {
	if len(raw) == 0 {
		raw = fallback
	}
	out := make([]string, len(raw))
	for i, s := range raw {
		out[i] = strings.ToLower(s)
	}
	return out
}

func lowerSingle(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func toSet(items []string) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, s := range items {
		set[s] = struct{}{}
	}
	return set
}

func toSetLower(items []string) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, s := range items {
		set[strings.ToLower(s)] = struct{}{}
	}
	return set
}

func isTerminal(state string, terminal []string) bool {
	return slices.Contains(terminal, state)
}

func isActive(state string, active []string) bool {
	return slices.Contains(active, state)
}

func intToStr(n int64) string {
	return strconv.FormatInt(n, 10)
}
