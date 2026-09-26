package orchestrator

import (
	"context"
	"log/slog"

	"github.com/sortie-ai/sortie/internal/config"
)

// LogAdvisories writes one Warn record per entry of advisories, in
// order, through logger.LogAttrs. An empty or nil slice writes
// nothing.
func LogAdvisories(ctx context.Context, logger *slog.Logger, advisories []config.Advisory) {
	for _, a := range advisories {
		logger.LogAttrs(ctx, slog.LevelWarn, a.Message, a.Attrs...) //nolint:sloglint // Advisory.Message comes from one of a fixed catalogue of string constants
	}
}

// advisoryEqual reports whether a and b are the same advisory: equal
// Message, and Attrs equal key by key and value by value, in declared
// order. Check and Text take no part.
func advisoryEqual(a, b config.Advisory) bool {
	if a.Message != b.Message {
		return false
	}
	if len(a.Attrs) != len(b.Attrs) {
		return false
	}
	for i := range a.Attrs {
		if a.Attrs[i].Key != b.Attrs[i].Key {
			return false
		}
		if !a.Attrs[i].Value.Equal(b.Attrs[i].Value) {
			return false
		}
	}
	return true
}

// newAdvisories returns the members of current for which no member of
// previous is [advisoryEqual], preserving current's order.
func newAdvisories(current, previous []config.Advisory) []config.Advisory {
	var fresh []config.Advisory
	for _, a := range current {
		seen := false
		for _, p := range previous {
			if advisoryEqual(a, p) {
				seen = true
				break
			}
		}
		if !seen {
			fresh = append(fresh, a)
		}
	}
	return fresh
}
