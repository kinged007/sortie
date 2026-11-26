package orchestrator

import (
	"context"
	"errors"
	"strings"
)

// The texts a cancelled run records as its error, one per reason the
// orchestrator cancels a worker. Run history stores them verbatim and the
// sentence layer words them for a person.
const (
	// StallCancelError is recorded when stall detection cancelled the run.
	StallCancelError = "stall timeout exceeded"

	// ShutdownCancelError is recorded when the service shut down under the run.
	ShutdownCancelError = "service shutting down"

	// TerminalCancelErrorPrefix leads the error of a run cancelled because its
	// issue reached a terminal tracker state; the state name follows.
	TerminalCancelErrorPrefix = "issue reached terminal state: "

	// InactiveCancelErrorPrefix leads the error of a run cancelled because its
	// issue left the active tracker states without becoming terminal; the state
	// name follows when the tracker reported one.
	InactiveCancelErrorPrefix = "issue left the active states: "
)

var (
	errCancelStall    = errors.New(StallCancelError)
	errCancelShutdown = errors.New(ShutdownCancelError)
)

func terminalCancelCause(stateName string) error {
	return errors.New(TerminalCancelErrorPrefix + stateName)
}

func inactiveCancelCause(stateName string) error {
	return errors.New(strings.TrimSuffix(InactiveCancelErrorPrefix+stateName, ": "))
}

// cancelRun cancels the entry's run context and records cause as the reason
// the worker observes through [context.Cause]. An entry built without
// [DispatchIssue] falls back to CancelFunc.
func (e *RunningEntry) cancelRun(cause error) {
	switch {
	case e.CancelCauseFunc != nil:
		e.CancelCauseFunc(cause)
	case e.CancelFunc != nil:
		e.CancelFunc()
	}
}

// cancelRunError returns the error a cancelled run records for the cause its
// worker context carried, or nil when the cause is a budget ceiling's, which
// records its own error, or when none was carried. A plain context
// cancellation comes from the parent context ending at shutdown.
func cancelRunError(cause error) error {
	switch {
	case cause == nil, errors.Is(cause, errCeilingStop):
		return nil
	case errors.Is(cause, context.Canceled):
		return errCancelShutdown
	}
	return cause
}
