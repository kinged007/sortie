package github

import "testing"

func TestIsTerminalState(t *testing.T) {
	t.Parallel()

	terminal := []string{"done", "wontfix"}

	tests := []struct {
		name           string
		state          string
		terminalStates []string
		want           bool
	}{
		{"done is terminal", "done", terminal, true},
		{"wontfix is terminal", "wontfix", terminal, true},
		{"active state not terminal", "in-progress", terminal, false},
		{"empty state not terminal", "", terminal, false},
		{"empty terminal list", "done", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := isTerminalState(tt.state, tt.terminalStates)
			if got != tt.want {
				t.Errorf("isTerminalState(%q) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}
