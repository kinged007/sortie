package server

import (
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "0s"},
		{"negative", -5 * time.Second, "0s"},
		{"45 seconds", 45 * time.Second, "45s"},
		{"12m 34s", 12*time.Minute + 34*time.Second, "12m 34s"},
		{"2h 15m 30s", 2*time.Hour + 15*time.Minute + 30*time.Second, "2h 15m 30s"},
		{"1d 3h 12m", 27*time.Hour + 12*time.Minute + 45*time.Second, "1d 3h 12m"},
		{"exact 1 hour", 1 * time.Hour, "1h 0m 0s"},
		{"exact 1 day", 24 * time.Hour, "1d 0h 0m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := FormatDuration(tt.d)
			if got != tt.want {
				t.Errorf("FormatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestFormatInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input int64
		want  string
	}{
		{"zero", 0, "0"},
		{"small", 999, "999"},
		{"one thousand", 1000, "1,000"},
		{"large", 1234567, "1,234,567"},
		{"negative", -1234, "-1,234"},
		{"negative large", -1234567, "-1,234,567"},
		{"exact boundary", 1000000, "1,000,000"},
		{"single digit", 7, "7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := FormatInt(tt.input)
			if got != tt.want {
				t.Errorf("FormatInt(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
