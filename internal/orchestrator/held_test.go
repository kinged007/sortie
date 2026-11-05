package orchestrator

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
)

func TestHeld(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	t2 := t1.Add(time.Minute)
	issue := domain.Issue{ID: "id-1", Identifier: "MT-1", Title: "Held"}

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"Since survives a later pass and a reason change", func(t *testing.T) {
			first := newHeldPass(nil, t0, nil, nil)
			first.hold(issue, HeldEntry{Reason: SkipBlockersNotRead})
			second := newHeldPass(first.entries(), t1, nil, nil)
			second.hold(issue, HeldEntry{Reason: SkipBlockedBy})

			got := second.entries()[issue.ID]

			if got.Since != t0 || got.Reason != SkipBlockedBy {
				t.Errorf("hold after a second pass = Since %v, Reason %q, want Since %v, Reason %q", got.Since, got.Reason, t0, SkipBlockedBy)
			}
		}},
		{"Since restarts after a pass that did not hold the issue", func(t *testing.T) {
			first := newHeldPass(nil, t0, nil, nil)
			first.hold(issue, HeldEntry{Reason: SkipBlockedBy})
			gap := newHeldPass(first.entries(), t1, nil, nil)
			third := newHeldPass(gap.entries(), t2, nil, nil)
			third.hold(issue, HeldEntry{Reason: SkipBlockedBy})

			got := third.entries()[issue.ID]

			if got.Since != t2 {
				t.Errorf("hold Since after a pass that skipped the issue = %v, want %v", got.Since, t2)
			}
		}},
		{"a pass holds an issue ID once", func(t *testing.T) {
			pass := newHeldPass(nil, t0, nil, nil)
			pass.hold(issue, HeldEntry{Reason: SkipSSHHostsFull})
			pass.hold(issue, HeldEntry{Reason: SkipBlockedBy})

			got := pass.entries()

			if len(got) != 1 || got[issue.ID].Reason != SkipSSHHostsFull {
				t.Errorf("entries() = %d records, first Reason %q, want 1 record with Reason %q", len(got), got[issue.ID].Reason, SkipSSHHostsFull)
			}
		}},
		{"blockers that hold are the empty-state and open ones, in order", func(t *testing.T) {
			blockers := []domain.BlockerRef{
				{ID: "a", State: "Done"},
				{ID: "b"},
				{ID: "c", State: "In Progress"},
				{ID: "d", State: "DONE"},
				{ID: "e", State: "To Do"},
			}

			got := nonTerminalBlockers(blockers, map[string]struct{}{"done": {}})

			var ids []string
			for _, blocker := range got {
				ids = append(ids, blocker.ID)
			}
			if want := []string{"b", "c", "e"}; !slices.Equal(ids, want) {
				t.Errorf("nonTerminalBlockers() IDs = %v, want %v", ids, want)
			}
		}},
		{"an issue on a list active merges is not listed held", func(t *testing.T) {
			lists := map[string]func(*State){
				"running":          func(s *State) { s.Running[issue.ID] = &RunningEntry{} },
				"retrying":         func(s *State) { s.RetryAttempts[issue.ID] = &RetryEntry{} },
				"budget exhausted": func(s *State) { s.BudgetExhausted[issue.ID] = &BudgetExhaustedEntry{} },
				"parked":           func(s *State) { s.Parked[issue.ID] = &ParkedEntry{} },
				"pending reaction": func(s *State) { s.PendingReactions[issue.ID+":ci"] = &PendingReaction{IssueID: issue.ID} },
				"between stages":   func(s *State) { s.StageHops[issue.ID] = &StageHopEntry{} },
			}
			for name, put := range lists {
				state := NewState(1000, 1, 0, nil, AgentTotals{})
				put(state)
				if listedHeld(state, issue.ID) {
					t.Errorf("listedHeld(issue on %s list) = true, want false", name)
				}
			}

			free := NewState(1000, 1, 0, nil, AgentTotals{})
			free.StageHops[issue.ID] = &StageHopEntry{RunDispatched: true}
			if !listedHeld(free, issue.ID) {
				t.Error("listedHeld(issue whose hop record was dispatched) = false, want true")
			}
		}},
		{"a between-stages issue shows its hold on the row only", func(t *testing.T) {
			state := NewState(1000, 1, 0, nil, AgentTotals{})
			state.StageHops[issue.ID] = &StageHopEntry{Identifier: issue.Identifier}
			pass := newHeldPass(nil, t0, nil, nil)
			pass.hold(issue, HeldEntry{Reason: SkipStageHopUnconfirmed})
			state.Held = pass.entries()

			snap := RuntimeSnapshot(state, t1)

			if len(snap.Held) != 0 {
				t.Errorf("RuntimeSnapshot().Held = %d rows, want none", len(snap.Held))
			}
			if len(snap.BetweenStages) != 1 || snap.BetweenStages[0].Hold == nil || snap.BetweenStages[0].Hold.Reason != SkipStageHopUnconfirmed {
				t.Errorf("RuntimeSnapshot().BetweenStages = %+v, want one row holding %q", snap.BetweenStages, SkipStageHopUnconfirmed)
			}
		}},
		{"held rows are never nil, carry a labels list, and sort by identifier then issue ID", func(t *testing.T) {
			state := NewState(1000, 1, 0, nil, AgentTotals{})
			if got := snapshotHeld(state); got == nil {
				t.Error("snapshotHeld(no records) = nil, want an empty slice")
			}
			pass := newHeldPass(nil, t0, nil, nil)
			for _, held := range []domain.Issue{
				{ID: "a", Identifier: "MT-2"},
				{ID: "z", Identifier: "MT-1"},
				{ID: "b", Identifier: "MT-1"},
			} {
				pass.hold(held, HeldEntry{Reason: SkipBlockedBy})
			}
			state.Held = pass.entries()

			var ids []string
			for _, row := range snapshotHeld(state) {
				ids = append(ids, row.IssueID)
				if row.IssueLabels == nil {
					t.Errorf("snapshotHeld() row %s IssueLabels = nil, want an empty list", row.IssueID)
				}
			}

			if want := []string{"b", "z", "a"}; !slices.Equal(ids, want) {
				t.Errorf("snapshotHeld() IssueIDs = %v, want %v", ids, want)
			}
		}},
		{"a registered secret is masked in the title and in blocker and slot states", func(t *testing.T) {
			secret := registeredSecret(t)
			state := NewState(1000, 1, 0, nil, AgentTotals{})
			pass := newHeldPass(nil, t0, nil, nil)
			pass.hold(domain.Issue{ID: "id-b", Identifier: "MT-1", Title: "leaks " + secret}, HeldEntry{
				Reason:   SkipBlockedBy,
				Blockers: []domain.BlockerRef{{ID: "x", State: secret}},
			})
			pass.hold(domain.Issue{ID: "id-s", Identifier: "MT-2"}, HeldEntry{
				Reason: SkipAgentSlotsFull,
				Slot:   &SlotHold{Scope: SlotScopeState, State: secret, Used: 1, Limit: 1},
			})
			state.Held = pass.entries()

			for _, row := range snapshotHeld(state) {
				served := row.IssueTitle
				if row.Hold.Blockers != nil {
					served += row.Hold.Blockers[0].State
				}
				if row.Hold.Slot != nil {
					served += row.Hold.Slot.State
				}
				if strings.Contains(served, secret) {
					t.Errorf("snapshotHeld() row %s serves %q, want the registered secret masked", row.IssueID, served)
				}
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.run(t)
		})
	}
}
