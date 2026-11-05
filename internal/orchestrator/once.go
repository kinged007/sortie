package orchestrator

import (
	"cmp"
	"slices"

	"github.com/sortie-ai/sortie/internal/domain"
)

// OnceResult reports how a [Orchestrator.Run] started with
// [OrchestratorParams.Once] ended. The zero value means the run settled with
// nothing to report.
type OnceResult struct {
	// Interrupted reports that Run returned through context cancellation.
	Interrupted bool

	// PollFailed reports that the single poll's preflight failed or its
	// candidate fetch errored while the context was live.
	PollFailed bool

	// Issues counts the issues with at least one recorded outcome.
	Issues int

	// NotSucceeded lists, sorted, the identifiers (issue IDs when the
	// identifier is empty) of issues whose latest outcome is not
	// [RunOutcomeSucceeded].
	NotSucceeded []string

	// NotRun counts the single poll's candidates with no recorded outcome.
	NotRun int

	// Outcomes lists, sorted by identifier, the latest outcome of every
	// issue with a recorded outcome. It is nil when none was recorded.
	Outcomes []OnceOutcome
}

// OnceOutcome is the latest outcome once mode recorded for one issue.
type OnceOutcome struct {
	// Identifier is the issue identifier, or the issue ID when the
	// identifier is empty.
	Identifier string

	// Outcome is the latest recorded outcome.
	Outcome RunOutcome
}

// deferKind says what a deferred single-poll candidate waits for.
type deferKind int

const (
	// deferSlot waits for capacity that a finishing session frees.
	deferSlot deferKind = iota + 1
	// deferTick waits for the next tick to evaluate it again.
	deferTick
)

// onceState is the bookkeeping of a once-mode run. The event-loop goroutine
// owns it; [Orchestrator.OnceResult] reads it only after Run returns. An
// orchestrator outside once mode holds a nil *onceState, and the methods the
// tick and the exit handlers call unguarded do nothing on that receiver.
type onceState struct {
	polled      bool
	pollFailed  bool
	interrupted bool
	candidates  map[string]struct{}
	deferred    map[string]deferKind
	outcomes    map[string]OnceOutcome
}

func newOnceState() *onceState {
	return &onceState{
		candidates: make(map[string]struct{}),
		deferred:   make(map[string]deferKind),
		outcomes:   make(map[string]OnceOutcome),
	}
}

func (s *onceState) record(issueID, identifier string, outcome RunOutcome) {
	if s == nil {
		return
	}
	s.outcomes[issueID] = OnceOutcome{Identifier: identifier, Outcome: outcome}
}

func (s *onceState) hasOutcome(issueID string) bool {
	_, ok := s.outcomes[issueID]
	return ok
}

// pollNeeded reports whether a tick has candidates to fetch: the single poll
// has not run, or a candidate of it still waits.
func (s *onceState) pollNeeded() bool {
	return s == nil || !s.polled || len(s.deferred) > 0
}

// endPoll marks the single poll as run, failed or not. Only the first call
// counts.
func (s *onceState) endPoll(failed bool) {
	if s == nil || s.polled {
		return
	}
	s.polled = true
	s.pollFailed = failed
}

// setCandidates remembers the candidates of the single poll. It ignores
// every later fetch.
func (s *onceState) setCandidates(issues []domain.Issue) {
	if s == nil || s.polled {
		return
	}
	for _, issue := range issues {
		s.candidates[issue.ID] = struct{}{}
	}
}

func (s *onceState) deferFor(issueID string, kind deferKind) {
	if s == nil {
		return
	}
	s.deferred[issueID] = kind
}

func (s *onceState) deferAll(issues []domain.Issue, kind deferKind) {
	for _, issue := range issues {
		s.deferFor(issue.ID, kind)
	}
}

// drop forgets a candidate that no later tick of this process dispatches,
// whether it was dispatched or declined.
func (s *onceState) drop(issueID string) {
	if s == nil {
		return
	}
	delete(s.deferred, issueID)
}

// declined updates the bookkeeping for a candidate the evaluation did not
// dispatch. A blocker read that failed or was not made waits for a later
// tick; a spend window or usage-limit hold ends the issue's part of the run
// as stopped; every other reason is final.
func (s *onceState) declined(issue domain.Issue, reason SkipReason) {
	switch {
	case reason == SkipBlockersNotRead || reason == SkipBlockersUnresolved:
		s.deferFor(issue.ID, deferTick)
	case isSpendCeilingReason(reason) || reason == SkipUsageLimit:
		s.drop(issue.ID)
		s.record(issue.ID, issue.Identifier, RunOutcomeStopped)
	default:
		s.drop(issue.ID)
	}
}

// dispatchFailed ends an issue's part of the run as failed, for a candidate
// whose agent kind, template, or attempt settings could not be resolved.
func (s *onceState) dispatchFailed(issue domain.Issue) {
	s.drop(issue.ID)
	s.record(issue.ID, issue.Identifier, RunOutcomeFailed)
}

// dropSlotWaiters forgets every candidate waiting for capacity. It runs when
// no session is left to free any.
func (s *onceState) dropSlotWaiters() {
	if s == nil {
		return
	}
	for issueID, kind := range s.deferred {
		if kind == deferSlot {
			delete(s.deferred, issueID)
		}
	}
}

// dispatchInput returns the candidates a tick may dispatch: all of them on the
// single poll, and afterwards only the deferred ones. A deferred ID that the
// fetch no longer returns is dropped.
func (s *onceState) dispatchInput(sorted []domain.Issue) []domain.Issue {
	if s == nil || !s.polled {
		return sorted
	}
	input := make([]domain.Issue, 0, len(s.deferred))
	returned := make(map[string]struct{}, len(sorted))
	for _, issue := range sorted {
		returned[issue.ID] = struct{}{}
		if _, ok := s.deferred[issue.ID]; ok {
			input = append(input, issue)
		}
	}
	for issueID := range s.deferred {
		if _, ok := returned[issueID]; !ok {
			delete(s.deferred, issueID)
		}
	}
	return input
}

// settled reports whether the single poll ran and no issue is left running,
// claimed, waiting for a retry, or deferred.
func (s *onceState) settled(state *State) bool {
	return s.polled && len(s.deferred) == 0 &&
		len(state.Running) == 0 && len(state.Claimed) == 0 && len(state.RetryAttempts) == 0
}

func (s *onceState) result() OnceResult {
	result := OnceResult{
		Interrupted: s.interrupted,
		PollFailed:  s.pollFailed,
		Issues:      len(s.outcomes),
	}
	for issueID, recorded := range s.outcomes {
		identifier := cmp.Or(recorded.Identifier, issueID)
		result.Outcomes = append(result.Outcomes, OnceOutcome{Identifier: identifier, Outcome: recorded.Outcome})
		if recorded.Outcome != RunOutcomeSucceeded {
			result.NotSucceeded = append(result.NotSucceeded, identifier)
		}
	}
	slices.Sort(result.NotSucceeded)
	slices.SortFunc(result.Outcomes, func(a, b OnceOutcome) int { return cmp.Compare(a.Identifier, b.Identifier) })
	for issueID := range s.candidates {
		if !s.hasOutcome(issueID) {
			result.NotRun++
		}
	}
	return result
}

// OnceResult reports how a run started with [OrchestratorParams.Once] ended.
// It returns the zero value for an orchestrator built without Once, and its
// result is meaningful only after [Orchestrator.Run] returns.
func (o *Orchestrator) OnceResult() OnceResult {
	if o.once == nil {
		return OnceResult{}
	}
	return o.once.result()
}
