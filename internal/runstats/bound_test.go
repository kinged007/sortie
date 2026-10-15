package runstats

import (
	"testing"
	"time"
)

func TestParseRangeBound(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 7, 9, 15, 0, 0, time.UTC)
	tests := []struct {
		name string
		raw  string
		want time.Time
	}{
		{name: "date-only", raw: "2026-07-01", want: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
		{name: "positive duration", raw: "24h", want: time.Date(2026, 8, 6, 9, 15, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseRangeBound(tt.raw, now)

			if err != nil {
				t.Fatalf("ParseRangeBound(%q) unexpected error: %v", tt.raw, err)
			}
			if got == nil || !got.Equal(tt.want) {
				t.Errorf("ParseRangeBound(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}
