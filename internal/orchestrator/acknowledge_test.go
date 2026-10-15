package orchestrator

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
)

func currentAttentionIDs(o *Orchestrator, state *State) []string {
	health := o.Health()
	var ids []string
	for _, cause := range AttentionCauses(RuntimeSnapshot(state, time.Now()), &health) {
		ids = append(ids, cause.ID)
	}
	return ids
}

func soleAttentionID(t *testing.T, o *Orchestrator, state *State) string {
	t.Helper()

	ids := currentAttentionIDs(o, state)
	if len(ids) != 1 {
		t.Fatalf("attention item IDs = %v, want exactly one", ids)
	}
	return ids[0]
}

func TestAcknowledgeOnLoop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		requests   int
		unknownID  bool
		saveErr    error
		wantStatus AcknowledgeStatus
		wantSaves  int
	}{
		{name: "current item is recorded", requests: 1, wantStatus: AcknowledgeRecorded, wantSaves: 1},
		{name: "repeat keeps the first time and saves once", requests: 2, wantStatus: AcknowledgeAlready, wantSaves: 1},
		{name: "unknown ID changes nothing", requests: 1, unknownID: true, wantStatus: AcknowledgeNotFound},
		{name: "failed save leaves the item unacknowledged", requests: 1, saveErr: errors.New("disk full"), wantStatus: AcknowledgeFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &stubStore{saveAcknowledgementErr: tt.saveErr}
			tracker := &candidateTrackerAdapter{mockTrackerAdapter: &mockTrackerAdapter{}}
			state := NewState(60000, 10, 0, nil, AgentTotals{})
			state.Parked["iss-1"] = &ParkedEntry{
				Identifier: "TEST-1",
				Reason:     "agent_blocked",
				ParkedAt:   time.Date(2026, 2, 24, 20, 0, 0, 0, time.UTC),
			}
			o := budgetOrchestrator(state, budgetTickConfig(0), store, tracker)
			itemID := soleAttentionID(t, o, state)
			requestID := itemID
			if tt.unknownID {
				requestID = "no-such-item"
			}

			var first, last AcknowledgeResult
			for i := range tt.requests {
				last = o.acknowledgeOnLoop(context.Background(), requestID)
				if i == 0 {
					first = last
				}
			}

			if last.Status != tt.wantStatus {
				t.Errorf("acknowledgeOnLoop(%q).Status = %d, want %d", requestID, last.Status, tt.wantStatus)
			}
			if got := len(store.savedAcknowledgements); got != tt.wantSaves {
				t.Errorf("SaveAttentionAcknowledgement calls = %d, want %d", got, tt.wantSaves)
			}
			heldAt, held := RuntimeSnapshot(state, time.Now()).AttentionAcknowledgements[itemID]
			wantHeld := tt.wantSaves > 0
			if held != wantHeld {
				t.Fatalf("RuntimeSnapshot AttentionAcknowledgements[%q] present = %v, want %v", itemID, held, wantHeld)
			}
			if wantHeld {
				if first.AcknowledgedAt.IsZero() || !heldAt.Equal(first.AcknowledgedAt) {
					t.Errorf("RuntimeSnapshot AttentionAcknowledgements[%q] = %v, want %v", itemID, heldAt, first.AcknowledgedAt)
				}
				if !last.AcknowledgedAt.Equal(first.AcknowledgedAt) {
					t.Errorf("acknowledgeOnLoop(%q).AcknowledgedAt = %v, want first time %v", requestID, last.AcknowledgedAt, first.AcknowledgedAt)
				}
			}

			tracker.commentMu.Lock()
			comments := len(tracker.commentCalls)
			tracker.commentMu.Unlock()
			if writes := len(tracker.transitionCalls) + comments; writes != 0 {
				t.Errorf("tracker transitions and comments = %d, want 0", writes)
			}
		})
	}
}

func TestAcknowledgementsAcrossTicks(t *testing.T) {
	t.Parallel()

	type candidateRead int
	const (
		issueListed candidateRead = iota
		issueOmitted
		fetchFailed
	)

	issue := domain.Issue{ID: "iss-1", Identifier: "TEST-1", Title: "title", State: "To Do"}

	tests := []struct {
		name        string
		restart     bool
		reads       []candidateRead
		wantHeld    bool
		wantDeleted bool
	}{
		{name: "failed fetch keeps the acknowledgement", reads: []candidateRead{fetchFailed}, wantHeld: true},
		{name: "candidates without the issue delete the acknowledgement", reads: []candidateRead{issueOmitted}, wantDeleted: true},
		{name: "hold that returns at the same ceiling is unacknowledged", reads: []candidateRead{issueOmitted, issueListed}, wantDeleted: true},
		{name: "restart keeps the acknowledgement through a failed fetch", restart: true, reads: []candidateRead{fetchFailed, issueListed}, wantHeld: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			store := &stubStore{budgetExhaustedIDs: map[string]int{issue.ID: 3}, scopeBudgetToCandidates: true}
			read := issueListed
			tracker := &candidateTrackerAdapter{
				mockTrackerAdapter: &mockTrackerAdapter{},
				fetchCandidatesFn: func(context.Context) ([]domain.Issue, error) {
					switch read {
					case issueOmitted:
						return nil, nil
					case fetchFailed:
						return nil, errors.New("tracker unavailable")
					}
					return []domain.Issue{issue}, nil
				},
			}
			state := NewState(60000, 10, 0, nil, AgentTotals{})
			o := budgetOrchestrator(state, budgetTickConfig(3), store, tracker)
			o.handleTick(ctx)
			itemID := soleAttentionID(t, o, state)
			acked := o.acknowledgeOnLoop(ctx, itemID)
			if acked.Status != AcknowledgeRecorded {
				t.Fatalf("acknowledgeOnLoop(%q).Status = %d, want %d", itemID, acked.Status, AcknowledgeRecorded)
			}

			if tt.restart {
				rows, err := store.ListAttentionAcknowledgements(ctx)
				if err != nil {
					t.Fatalf("ListAttentionAcknowledgements: %v", err)
				}
				state = NewState(60000, 10, 0, nil, AgentTotals{})
				PopulateAttentionAcknowledgements(state, rows, discardLogger())
				o = budgetOrchestrator(state, budgetTickConfig(3), store, tracker)
			}
			for _, read = range tt.reads {
				o.handleTick(ctx)
			}

			heldAt, held := state.AttentionAcknowledgements[itemID]
			if held != tt.wantHeld {
				t.Errorf("AttentionAcknowledgements[%q] present = %v, want %v", itemID, held, tt.wantHeld)
			}
			if held && !heldAt.Equal(acked.AcknowledgedAt) {
				t.Errorf("AttentionAcknowledgements[%q] = %v, want %v", itemID, heldAt, acked.AcknowledgedAt)
			}
			if deleted := slices.Contains(store.deletedAcknowledgementIDs, itemID); deleted != tt.wantDeleted {
				t.Errorf("DeleteAttentionAcknowledgements called with %q = %v, want %v", itemID, deleted, tt.wantDeleted)
			}
			wantDerived := tt.reads[len(tt.reads)-1] != issueOmitted
			if derived := slices.Contains(currentAttentionIDs(o, state), itemID); derived != wantDerived {
				t.Errorf("attention item %q derived after %v = %v, want %v", itemID, tt.reads, derived, wantDerived)
			}
		})
	}
}
