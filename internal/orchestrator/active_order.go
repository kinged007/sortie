package orchestrator

import "time"

// activeOrderOf returns the order time of every issue the row lists hold: the
// time already recorded in state.ActiveOrder, else the earliest dated row the
// issue has now, else the zero time. The returned map shares nothing with
// state.
func activeOrderOf(state *State) map[string]time.Time {
	first := make(map[string]time.Time)
	consider := func(issueID string, dispatchedAt time.Time) {
		known, listed := first[issueID]
		if !listed || known.IsZero() || (!dispatchedAt.IsZero() && dispatchedAt.Before(known)) {
			first[issueID] = dispatchedAt
		}
	}
	for id, entry := range state.Running {
		consider(id, entry.StageLineage.DispatchedAt)
	}
	for id, entry := range state.RetryAttempts {
		consider(id, entry.StageLineage.DispatchedAt)
	}
	for id, entry := range state.BudgetExhausted {
		consider(id, entry.StageLineage.DispatchedAt)
	}
	for id, entry := range state.Parked {
		consider(id, entry.StageLineage.DispatchedAt)
	}
	for _, entry := range state.PendingReactions {
		consider(entry.IssueID, entry.StageLineage.DispatchedAt)
	}
	for id, hop := range state.StageHops {
		if listedBetweenStages(state, id, hop) {
			consider(id, hop.DispatchedAt)
		}
	}

	order := make(map[string]time.Time, len(first))
	for id, at := range first {
		if recorded, ok := state.ActiveOrder[id]; ok {
			at = recorded
		}
		order[id] = at
	}
	return order
}

// refreshActiveOrder replaces state.ActiveOrder with the places of the issues
// the row lists hold now. It runs on the event-loop goroutine only.
func refreshActiveOrder(state *State) {
	state.ActiveOrder = activeOrderOf(state)
}
