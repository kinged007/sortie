package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
)

// Reasons a pending reaction waits for its next check.
const (
	waitPollInterval = "poll_interval"
	waitDebounce     = "debounce"
	waitBackoff      = "backoff"
	waitRetrySlot    = "retry_slot"
	waitTriage       = "triage"
)

// requeueReaction returns entry to the pending set under key and records why
// it waits for its next check.
//
// A check that a request brought forward gives back the due time and backoff
// count the entry had before the request, so asking early never delays a
// later check.
func requeueReaction(state *State, key string, entry *PendingReaction, wait string) {
	if !entry.dueBeforeRequest.IsZero() {
		entry.PendingRetryAt = entry.dueBeforeRequest
		entry.PendingAttempts = min(entry.PendingAttempts, entry.attemptsBeforeRequest)
		entry.dueBeforeRequest = time.Time{}
		entry.attemptsBeforeRequest = 0
	}
	entry.Wait = wait
	state.PendingReactions[key] = entry
}

// reactionWaits reports whether entry's next check is still in the future at
// now. Once now reaches the due time a request replaced, it forgets that due
// time, because the check is one the entry had anyway.
func reactionWaits(entry *PendingReaction, now time.Time) bool {
	if !entry.dueBeforeRequest.IsZero() && !now.Before(entry.dueBeforeRequest) {
		entry.dueBeforeRequest = time.Time{}
		entry.attemptsBeforeRequest = 0
	}
	return now.Before(entry.PendingRetryAt)
}

// notePullRequestURL keeps the link status reports; an empty one changes
// nothing.
func notePullRequestURL(entry *PendingReaction, status domain.PRMergeStatus) {
	if status.URL != "" {
		entry.PullRequestURL = status.URL
	}
}

// readPullRequestURL reads entry's pull request once and keeps its link when
// entry holds none. A failed read is logged at Debug and changes nothing
// else: no backoff, no wait reason, no PendingAttempts.
func readPullRequestURL(ctx context.Context, scm domain.SCMAdapter, entry *PendingReaction, log *slog.Logger) {
	if entry.PullRequestURL != "" {
		return
	}
	pr := pullRequestOf(entry)
	status, err := scm.GetMergeability(ctx, pr.Number, pr.Owner, pr.Repo)
	if err != nil {
		log.DebugContext(ctx, "pull request link read failed", slog.Any("error", err))
		return
	}
	notePullRequestURL(entry, status)
}

// pullRequestRef is the pull request a pending reaction's kind data records.
type pullRequestRef struct {
	Number  int
	Owner   string
	Repo    string
	Branch  string
	HeadSHA string
}

// pullRequestOf returns the pull request entry's kind data records, branch
// and head empty where the kind keeps none; the zero value for other data.
func pullRequestOf(entry *PendingReaction) pullRequestRef {
	switch data := entry.KindData.(type) {
	case *CIReactionData:
		return pullRequestRef{Number: data.PRNumber, Owner: data.Owner, Repo: data.Repo, Branch: data.Branch, HeadSHA: data.SHA}
	case *ReviewReactionData:
		return pullRequestRef{Number: data.PRNumber, Owner: data.Owner, Repo: data.Repo, Branch: data.Branch, HeadSHA: data.SHA}
	case *BotReviewReactionData:
		return pullRequestRef{Number: data.PRNumber, Owner: data.Owner, Repo: data.Repo, Branch: data.Branch, HeadSHA: data.SHA}
	case *AutoMergeReactionData:
		return pullRequestRef{Number: data.PRNumber, Owner: data.Owner, Repo: data.Repo, Branch: data.Branch, HeadSHA: data.SHA}
	case *MergeConflictReactionData:
		return pullRequestRef{Number: data.PRNumber, Owner: data.Owner, Repo: data.Repo, Branch: data.Branch, HeadSHA: data.SHA}
	case *LabelReviewReactionData:
		return pullRequestRef{Number: data.PRNumber, Owner: data.Owner, Repo: data.Repo}
	case *LabelFixReactionData:
		return pullRequestRef{Number: data.PRNumber, Owner: data.Owner, Repo: data.Repo, Branch: data.Branch}
	case *MergeCompletionReactionData:
		return pullRequestRef{Number: data.PRNumber, Owner: data.Owner, Repo: data.Repo}
	default:
		return pullRequestRef{}
	}
}

// continuationTargetOf returns the target of the continuation that fixes the
// pull request entry watches, on the given try of its reaction.
func continuationTargetOf(entry *PendingReaction, attempt int) ContinuationTarget {
	pr := pullRequestOf(entry)
	return ContinuationTarget{PRNumber: pr.Number, Owner: pr.Owner, Repo: pr.Repo, Attempt: attempt}
}

// watchWindowStart returns when entry's watch window began: its last
// retry-slot deferral, or its creation.
func watchWindowStart(entry *PendingReaction) time.Time {
	if !entry.WatchRestartedAt.IsZero() {
		return entry.WatchRestartedAt
	}
	return entry.CreatedAt
}
