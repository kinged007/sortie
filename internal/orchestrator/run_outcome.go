package orchestrator

import (
	"context"
	"errors"

	"github.com/sortie-ai/sortie/internal/domain"
)

// RunOutcome is how one attempt ended, grouped the way the dashboard groups a
// finished run.
type RunOutcome string

// The stopped outcome covers an attempt that needed input, stopped at a budget
// or a usage limit, or ended with its agent declaring blocked.
const (
	RunOutcomeSucceeded RunOutcome = "succeeded"
	RunOutcomeStopped   RunOutcome = "stopped"
	RunOutcomeFailed    RunOutcome = "failed"
	RunOutcomeCancelled RunOutcome = "cancelled"
)

// attemptStatus returns the run history status of an attempt that ended with
// the given exit kind and error. ceilingStop reports that a spend ceiling
// stopped the attempt.
func attemptStatus(kind WorkerExitKind, ceilingStop bool, err error) string {
	status := mapExitKindToStatus(kind)
	if kind == WorkerExitCancelled && ceilingStop {
		return "budget_stopped"
	}
	// A needs-a-person ending gets its own status only when the worker's own
	// error, not a shutdown racing it, stopped the run. The error is always
	// wrapped, so it MUST be unwrapped rather than asserted.
	if status == "failed" {
		if agentErr, ok := errors.AsType[*domain.AgentError](err); ok && agentErr.Kind == domain.ErrTurnInputRequired {
			return "needs_person"
		}
	}
	return status
}

// runOutcomeOf groups a run history status and the agent's declared outcome
// token into a [RunOutcome]. An unknown status reads as failed.
func runOutcomeOf(status, declared string) RunOutcome {
	switch status {
	case "succeeded":
		if declared == "blocked" {
			return RunOutcomeStopped
		}
		return RunOutcomeSucceeded
	case "needs_person", "budget_stopped", "usage_limited":
		return RunOutcomeStopped
	case "cancelled":
		return RunOutcomeCancelled
	default:
		return RunOutcomeFailed
	}
}

// workerRunOutcome returns the outcome a worker exiting with the given kind,
// error, and declared token will be recorded under. It derives the ceiling
// stop from the context cause, as the worker's result does.
func workerRunOutcome(ctx context.Context, kind WorkerExitKind, err error, declared string) RunOutcome {
	ceilingStop := kind == WorkerExitCancelled && errors.Is(context.Cause(ctx), errCeilingStop)
	return runOutcomeOf(attemptStatus(kind, ceilingStop, err), declared)
}
