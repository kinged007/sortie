package issuekit

import "testing"

func testLabelStates() LabelStates {
	return LabelStates{
		Active:   []string{"backlog", "in-progress", "review"},
		Terminal: []string{"done", "wontfix"},
		Handoff:  "awaiting-human",
	}
}

func TestLabelStates_Derive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		labels []string
		closed bool
		want   string
	}{
		{"terminal outranks handoff and active", []string{"backlog", "awaiting-human", "done"}, false, "done"},
		{"terminal follows configured order", []string{"wontfix", "done"}, false, "done"},
		{"handoff outranks active", []string{"review", "awaiting-human"}, false, "awaiting-human"},
		{"active follows configured order", []string{"review", "backlog"}, false, "backlog"},
		{"label case is ignored", []string{"Backlog"}, false, "backlog"},
		{"open without a state label reads no state", []string{"bug"}, false, ""},
		{"closed with an active label reads the first terminal state", []string{"backlog"}, true, "done"},
		{"closed keeps the terminal label it carries", []string{"wontfix", "backlog"}, true, "wontfix"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, _ := testLabelStates().Derive(tt.labels, tt.closed)

			if got != tt.want {
				t.Errorf("Derive(%v, closed=%v) = %q, want %q", tt.labels, tt.closed, got, tt.want)
			}
		})
	}
}
