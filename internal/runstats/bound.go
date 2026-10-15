package runstats

import (
	"fmt"
	"time"
)

// ParseRangeBound parses a since or until value into a UTC time bound. An
// empty raw returns nil, nil, meaning the bound is absent. raw MUST be an
// RFC 3339 timestamp, a YYYY-MM-DD date (read as 00:00:00Z on that date), or
// a positive Go duration (read as now minus that duration); any other input,
// including a zero or negative duration, returns an error.
func ParseRangeBound(raw string, now time.Time) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return new(t.UTC()), nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return new(t.UTC()), nil
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return new(now.UTC().Add(-d)), nil
	}
	return nil, fmt.Errorf(
		"invalid range bound %q: accepts an RFC3339 timestamp (2026-07-01T00:00:00Z), "+
			"a date (2026-07-01), or a positive duration (24h)", raw)
}
