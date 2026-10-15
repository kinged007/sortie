package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/sortie-ai/sortie/internal/logging"
	"github.com/sortie-ai/sortie/internal/redact"
)

// CheckNowStatus is what a request to check a pending pull request reaction
// now did.
type CheckNowStatus int

const (
	CheckNowDue       CheckNowStatus = iota + 1 // the reaction's next check is due at once
	CheckNowNotFound                            // no pending reaction has the identifier and kind; nothing changed
	CheckNowDraining                            // shutdown began before the loop took the request; nothing changed
	CheckNowWithdrawn                           // the caller's context ended before the loop took the request; nothing changed
)

// CheckNowResult is the answer to a request to check a pending pull request
// reaction now. Every field but Status is set only with [CheckNowDue].
type CheckNowResult struct {
	Status       CheckNowStatus
	IssueID      string
	Identifier   string
	DisplayID    string
	Kind         string
	PRNumber     int
	PRRepository string // owner/repo, masked as the pending reaction row masks it
}

// CheckNowFunc returns a function that makes the next check of the pending
// pull request reaction of the given kind on the issue with the given
// identifier due now. The function is safe for concurrent use and returns
// once the event loop has taken the request, ctx has ended, or shutdown has
// begun.
func (o *Orchestrator) CheckNowFunc() func(ctx context.Context, identifier, kind string) CheckNowResult {
	return func(ctx context.Context, identifier, kind string) CheckNowResult {
		var result CheckNowResult
		switch o.runOnLoop(ctx, func(context.Context) {
			result = o.checkNowOnLoop(identifier, kind, time.Now().UTC())
		}) {
		case loopRan:
			return result
		case loopDraining:
			return CheckNowResult{Status: CheckNowDraining}
		default:
			return CheckNowResult{Status: CheckNowWithdrawn}
		}
	}
}

// checkNowOnLoop moves the pending reaction's check to now when it is later
// and records the replaced schedule for the pass to put back. Nothing else
// about the entry changes, so the next pass sees what a pass at the due time
// would. Must run on the event loop.
func (o *Orchestrator) checkNowOnLoop(identifier, kind string, now time.Time) CheckNowResult {
	var entry *PendingReaction
	for _, e := range o.state.PendingReactions {
		if e.Identifier == identifier && e.Kind == kind {
			entry = e
			break
		}
	}
	if entry == nil {
		return CheckNowResult{Status: CheckNowNotFound}
	}

	if now.Before(entry.PendingRetryAt) {
		entry.dueBeforeRequest = entry.PendingRetryAt
		entry.attemptsBeforeRequest = entry.PendingAttempts
		entry.PendingRetryAt = now
		logging.WithIssue(o.logger, entry.IssueID, entry.Identifier).Info("reaction check made due on request",
			slog.String("reaction_kind", entry.Kind),
			slog.Time("replaced_next_poll_at", entry.dueBeforeRequest),
		)
		o.notifyObservers()
	}

	pr := pullRequestOf(entry)
	return CheckNowResult{
		Status:       CheckNowDue,
		IssueID:      entry.IssueID,
		Identifier:   entry.Identifier,
		DisplayID:    entry.DisplayID,
		Kind:         entry.Kind,
		PRNumber:     pr.Number,
		PRRepository: redact.Mask(pr.Owner + "/" + pr.Repo),
	}
}
