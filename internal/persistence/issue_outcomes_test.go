package persistence

import "testing"

func TestRecordIssueOutcome_Rule(t *testing.T) {
	t.Parallel()

	const first, second = "2026-02-24T21:00:00Z", "2026-02-24T21:05:00Z"
	outcome := func(kind, observedAt string) IssueOutcome {
		o := IssueOutcome{IssueID: "id-1", Kind: kind, ObservedAt: observedAt}
		switch kind {
		case IssueOutcomeMerged:
			o.MergeReactionKind, o.PRNumber, o.PRRepository = "merge", 88, "acme/api"
		case IssueOutcomeHandedOff:
			o.State = "In Review"
		case IssueOutcomeTerminal:
			o.State = "Done"
		}
		return o
	}

	tests := []struct {
		name, stored, incoming string
		wantIncoming           bool
	}{
		{"none then handed off", "", IssueOutcomeHandedOff, true},
		{"none then merged", "", IssueOutcomeMerged, true},
		{"none then terminal", "", IssueOutcomeTerminal, true},
		{"handed off then handed off", IssueOutcomeHandedOff, IssueOutcomeHandedOff, true},
		{"handed off then merged", IssueOutcomeHandedOff, IssueOutcomeMerged, true},
		{"handed off then terminal", IssueOutcomeHandedOff, IssueOutcomeTerminal, true},
		{"merged then handed off", IssueOutcomeMerged, IssueOutcomeHandedOff, true},
		{"merged then merged", IssueOutcomeMerged, IssueOutcomeMerged, false},
		{"merged then terminal", IssueOutcomeMerged, IssueOutcomeTerminal, false},
		{"terminal then handed off", IssueOutcomeTerminal, IssueOutcomeHandedOff, true},
		{"terminal then merged", IssueOutcomeTerminal, IssueOutcomeMerged, true},
		{"terminal then terminal", IssueOutcomeTerminal, IssueOutcomeTerminal, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := openTestStore(t)
			migrateOrFatal(t, s)
			ctx := t.Context()
			if tt.stored != "" {
				if err := s.RecordIssueOutcome(ctx, outcome(tt.stored, first)); err != nil {
					t.Fatalf("RecordIssueOutcome(stored %s): %v", tt.stored, err)
				}
			}

			if err := s.RecordIssueOutcome(ctx, outcome(tt.incoming, second)); err != nil {
				t.Fatalf("RecordIssueOutcome(incoming %s): %v", tt.incoming, err)
			}
			got, err := s.IssueOutcomes(ctx, []string{"id-1"})
			if err != nil {
				t.Fatalf("IssueOutcomes: %v", err)
			}

			want := outcome(tt.incoming, second)
			if !tt.wantIncoming {
				want = outcome(tt.stored, first)
			}
			if got["id-1"] != want {
				t.Errorf("IssueOutcomes after %s = %+v, want %+v", tt.name, got["id-1"], want)
			}
		})
	}
}
