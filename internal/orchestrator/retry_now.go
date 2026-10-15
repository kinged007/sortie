package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/sortie-ai/sortie/internal/logging"
)

// RetryNowStatus is what a request to run a scheduled retry now did.
type RetryNowStatus int

const (
	RetryNowDue       RetryNowStatus = iota + 1 // the retry was made due now
	RetryNowNotFound                            // no retry entry carries the identifier; nothing changed
	RetryNowDraining                            // shutdown began before the loop took the request; nothing changed
	RetryNowWithdrawn                           // the caller's context ended before the loop took the request; nothing changed
)

// RetryNowResult is the answer to a request to run a scheduled retry now.
// IssueID, Identifier, DisplayID, and Attempt are set only with RetryNowDue.
type RetryNowResult struct {
	Status     RetryNowStatus
	IssueID    string
	Identifier string
	DisplayID  string
	Attempt    int
}

// RetryNowFunc returns a function that makes the scheduled retry of the
// issue with the given identifier due now. The function is safe for
// concurrent use and returns once the event loop has taken the request,
// ctx has ended, or shutdown has begun.
func (o *Orchestrator) RetryNowFunc() func(ctx context.Context, identifier string) RetryNowResult {
	return func(ctx context.Context, identifier string) RetryNowResult {
		var result RetryNowResult
		switch o.runOnLoop(ctx, func(loopCtx context.Context) {
			result = o.retryNowOnLoop(loopCtx, identifier)
		}) {
		case loopRan:
			return result
		case loopDraining:
			return RetryNowResult{Status: RetryNowDraining}
		default:
			return RetryNowResult{Status: RetryNowWithdrawn}
		}
	}
}

// retryNowOnLoop re-arms the retry of the issue with the given identifier in
// place with a zero delay. A rebuilt entry would lose the runtime-only
// fields the fire reads, and the fire must find the entry a timer firing at
// its due time finds, so only the schedule changes. Must run on the event
// loop.
func (o *Orchestrator) retryNowOnLoop(ctx context.Context, identifier string) RetryNowResult {
	var issueID string
	var entry *RetryEntry
	for id, e := range o.state.RetryAttempts {
		if e.Identifier == identifier {
			issueID, entry = id, e
			break
		}
	}
	if entry == nil {
		return RetryNowResult{Status: RetryNowNotFound}
	}

	replaced := entry.DueAtMS
	if entry.dueBeforeRequestMS == 0 {
		entry.dueBeforeRequestMS = replaced
	}
	now := time.Now()
	if entry.TimerHandle != nil {
		entry.TimerHandle.Stop()
	}
	entry.scheduledAt = now
	entry.scheduledDelayMS = 0
	entry.DueAtMS = now.UnixMilli()
	entry.TimerHandle = time.AfterFunc(0, func() { o.onRetryFire(issueID) })

	log := logging.WithIssue(o.logger, issueID, entry.Identifier)
	if !isKnownReactionKind(entry.ReactionKind) {
		persistRetryEntry(ctx, log, o.store, o.state, issueID)
	}
	log.Info("retry made due on request",
		slog.Int("attempt", entry.Attempt),
		slog.Int64("replaced_due_at_ms", replaced),
	)
	o.notifyObservers()

	return RetryNowResult{
		Status:     RetryNowDue,
		IssueID:    issueID,
		Identifier: entry.Identifier,
		DisplayID:  entry.DisplayID,
		Attempt:    entry.Attempt,
	}
}
