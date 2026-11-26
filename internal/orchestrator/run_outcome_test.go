package orchestrator

import (
	"context"
	"fmt"
	"testing"

	"github.com/sortie-ai/sortie/internal/domain"
)

func TestRunOutcomeOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status   string
		declared string
		want     RunOutcome
	}{
		{"succeeded", "", RunOutcomeSucceeded},
		{"succeeded", "needs-human-review", RunOutcomeSucceeded},
		{"succeeded", "no-change-needed", RunOutcomeSucceeded},
		{"succeeded", "unrecognized", RunOutcomeSucceeded},
		{"succeeded", "blocked", RunOutcomeStopped},
		{"needs_person", "", RunOutcomeStopped},
		{"needs_person", "needs-human-review", RunOutcomeStopped},
		{"budget_stopped", "", RunOutcomeStopped},
		{"cancelled", "", RunOutcomeCancelled},
		{"cancelled", "blocked", RunOutcomeCancelled},
		{"failed", "", RunOutcomeFailed},
		{"failed", "blocked", RunOutcomeFailed},
		{"ci_failed", "", RunOutcomeFailed},
		{"unrecognized", "", RunOutcomeFailed},
	}

	for _, tt := range tests {
		t.Run(tt.status+"/"+tt.declared, func(t *testing.T) {
			t.Parallel()

			got := runOutcomeOf(tt.status, tt.declared)

			if got != tt.want {
				t.Errorf("runOutcomeOf(%q, %q) = %q, want %q", tt.status, tt.declared, got, tt.want)
			}
		})
	}
}

func TestAttemptStatus(t *testing.T) {
	t.Parallel()

	needsInput := fmt.Errorf("turn 1: %w", &domain.AgentError{Kind: domain.ErrTurnInputRequired})
	otherAgentErr := fmt.Errorf("turn 1: %w", &domain.AgentError{Kind: domain.ErrTurnFailed})
	tests := []struct {
		name        string
		kind        WorkerExitKind
		ceilingStop bool
		err         error
		want        string
	}{
		{"normal exit", WorkerExitNormal, false, nil, "succeeded"},
		{"error exit", WorkerExitError, false, fmt.Errorf("boom"), "failed"},
		{"cancel at a ceiling stop", WorkerExitCancelled, true, nil, "budget_stopped"},
		{"cancel without a ceiling stop", WorkerExitCancelled, false, nil, "cancelled"},
		{"ceiling stop flag on an error exit", WorkerExitError, true, fmt.Errorf("boom"), "failed"},
		{"wrapped turn input required", WorkerExitError, false, needsInput, "needs_person"},
		{"turn input required racing a cancel", WorkerExitCancelled, false, needsInput, "cancelled"},
		{"other agent error", WorkerExitError, false, otherAgentErr, "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := attemptStatus(tt.kind, tt.ceilingStop, tt.err)

			if got != tt.want {
				t.Errorf("attemptStatus(%q, %v, %v) = %q, want %q", tt.kind, tt.ceilingStop, tt.err, got, tt.want)
			}
		})
	}
}

func TestWorkerRunOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cause error
		kind  WorkerExitKind
		want  RunOutcome
	}{
		{"cancel caused by a ceiling stop", errCeilingStop, WorkerExitCancelled, RunOutcomeStopped},
		{"cancel caused by a shutdown", context.Canceled, WorkerExitCancelled, RunOutcomeCancelled},
		{"ceiling cause on an error exit", errCeilingStop, WorkerExitError, RunOutcomeFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(tt.cause)

			got := workerRunOutcome(ctx, tt.kind, nil, "")

			if got != tt.want {
				t.Errorf("workerRunOutcome(cause %v, %q) = %q, want %q", tt.cause, tt.kind, got, tt.want)
			}
		})
	}
}
