package githubpr

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/sortie-ai/sortie/internal/domain"
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

func classifyError(resp *http.Response, method, path string) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	_, _ = io.Copy(io.Discard, resp.Body)
	detail := string(snippet)

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return &domain.TrackerError{Kind: domain.ErrTrackerAuth, Message: fmt.Sprintf("%s %s: bad credentials", method, path), Status: resp.StatusCode}
	case resp.StatusCode == http.StatusForbidden:
		return &domain.TrackerError{Kind: domain.ErrTrackerAuth, Message: fmt.Sprintf("%s %s: insufficient permissions", method, path), Status: resp.StatusCode}
	case resp.StatusCode == http.StatusNotFound:
		return &domain.TrackerError{Kind: domain.ErrTrackerNotFound, Message: fmt.Sprintf("%s %s: not found", method, path), Status: resp.StatusCode}
	case resp.StatusCode >= 500:
		return &domain.TrackerError{Kind: domain.ErrTrackerTransport, Message: fmt.Sprintf("%s %s: server error %d: %s", method, path, resp.StatusCode, detail), Status: resp.StatusCode}
	default:
		return &domain.TrackerError{Kind: domain.ErrTrackerAPI, Message: fmt.Sprintf("%s %s: unexpected status %d: %s", method, path, resp.StatusCode, detail), Status: resp.StatusCode}
	}
}

func splitCut(s, sep string) (before, after string, found bool) {
	if i := strings.Index(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
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
	for _, t := range terminal {
		if state == t {
			return true
		}
	}
	return false
}

func isActive(state string, active []string) bool {
	for _, s := range active {
		if state == s {
			return true
		}
	}
	return false
}

func intToStr(n int64) string {
	return strconv.FormatInt(n, 10)
}
