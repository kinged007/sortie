package orchestrator

import (
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
)

func TestActiveOrderKeepsFirstPlaceAndHoldsOnlyListedIssues(t *testing.T) {
	t.Parallel()

	first := time.Date(2026, 2, 24, 19, 0, 0, 0, time.UTC)
	second, third := first.Add(time.Hour), first.Add(2*time.Hour)
	lineage := func(at time.Time) StageLineage { return StageLineage{DispatchedAt: at} }

	state := NewState(60000, 10, 0, nil, AgentTotals{})
	state.Running["run"] = &RunningEntry{Identifier: "MT-1", Issue: domain.Issue{ID: "run"}, StageLineage: lineage(second)}
	state.Running["dual"] = &RunningEntry{Identifier: "MT-2", Issue: domain.Issue{ID: "dual"}, StageLineage: lineage(third)}
	state.PendingReactions[ReactionKey("dual", "ci")] = &PendingReaction{IssueID: "dual", Identifier: "MT-2", Kind: "ci", StageLineage: lineage(first)}
	state.PendingReactions[ReactionKey("pending", "ci")] = &PendingReaction{IssueID: "pending", Identifier: "MT-3", Kind: "ci", StageLineage: lineage(second)}
	state.RetryAttempts["retry"] = &RetryEntry{IssueID: "retry", Identifier: "MT-4", StageLineage: lineage(second)}
	state.BudgetExhausted["budget"] = &BudgetExhaustedEntry{Identifier: "MT-5", StageLineage: lineage(second)}
	state.Parked["parked"] = &ParkedEntry{Identifier: "MT-6", StageLineage: lineage(second)}
	state.Parked["mover"] = &ParkedEntry{Identifier: "MT-7", StageLineage: lineage(first)}
	state.Parked["lost"] = &ParkedEntry{Identifier: "MT-8", StageLineage: lineage(second)}
	state.StageHops["hop"] = &StageHopEntry{Identifier: "MT-9", DispatchedAt: second}
	state.StageHops["closed"] = &StageHopEntry{Identifier: "MT-10", DispatchedAt: second, RunDispatched: true}
	refreshActiveOrder(state)

	delete(state.Parked, "mover")
	delete(state.Parked, "lost")
	state.Running["mover"] = &RunningEntry{Identifier: "MT-7", Issue: domain.Issue{ID: "mover"}, StageLineage: lineage(third)}
	refreshActiveOrder(state)
	snap := RuntimeSnapshot(state, third)

	wantIDs := []string{"budget", "dual", "hop", "mover", "parked", "pending", "retry", "run"}
	rowIDs := make(map[string]struct{})
	for _, row := range snap.Running {
		rowIDs[row.IssueID] = struct{}{}
	}
	for _, row := range snap.Retrying {
		rowIDs[row.IssueID] = struct{}{}
	}
	for _, row := range snap.BudgetExhausted {
		rowIDs[row.IssueID] = struct{}{}
	}
	for _, row := range snap.Parked {
		rowIDs[row.IssueID] = struct{}{}
	}
	for _, row := range snap.PendingReactions {
		rowIDs[row.IssueID] = struct{}{}
	}
	for _, row := range snap.BetweenStages {
		rowIDs[row.IssueID] = struct{}{}
	}
	for name, got := range map[string][]string{
		"State.ActiveOrder":    slices.Sorted(maps.Keys(state.ActiveOrder)),
		"snapshot ActiveOrder": slices.Sorted(maps.Keys(snap.ActiveOrder)),
		"snapshot row lists":   slices.Sorted(maps.Keys(rowIDs)),
	} {
		if !slices.Equal(got, wantIDs) {
			t.Errorf("issue IDs of %s = %v, want %v", name, got, wantIDs)
		}
	}

	for id, want := range map[string]time.Time{"mover": first, "dual": first, "run": second} {
		if got := state.ActiveOrder[id]; !got.Equal(want) {
			t.Errorf("State.ActiveOrder[%q] = %v, want %v", id, got, want)
		}
		if got := snap.ActiveOrder[id]; !got.Equal(want) {
			t.Errorf("snapshot ActiveOrder[%q] = %v, want %v", id, got, want)
		}
	}
}
