package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
)

func TestCheckNow(t *testing.T) {
	t.Parallel()

	const issueID = "ISS-CN-1"

	tests := []struct {
		name                string
		review              bool
		requestKind         string
		dueIn               time.Duration
		pendingAttempts     int
		reactionAttempts    int
		storedHead          string
		ciStatus            domain.CIStatus
		wantStatus          CheckNowStatus
		wantFetches         int
		wantRetry           bool
		wantEscalated       bool
		wantWait            string
		wantNextPollIn      time.Duration
		wantPendingAttempts *int
	}{
		{
			name: "far-off failing check fetches and schedules a fix on the next pass", requestKind: ReactionKindCI,
			dueIn: time.Hour, storedHead: ciDefaultHead, ciStatus: domain.CIStatusFailing,
			wantStatus: CheckNowDue, wantFetches: 1, wantRetry: true,
		},
		{
			name: "spent fix budget escalates instead of dispatching", requestKind: ReactionKindCI,
			dueIn: time.Hour, storedHead: ciDefaultHead, ciStatus: domain.CIStatusFailing,
			reactionAttempts: defaultCIFeedback().MaxRetries,
			wantStatus:       CheckNowDue, wantFetches: 1, wantEscalated: true,
		},
		{
			name: "fresh review comments still wait out the debounce", review: true, requestKind: ReactionKindReview,
			dueIn:      20 * time.Second,
			wantStatus: CheckNowDue, wantFetches: 1, wantWait: waitDebounce, wantNextPollIn: 20 * time.Second,
		},
		{
			name: "kind without a pending reaction changes nothing", requestKind: ReactionKindReview,
			dueIn: time.Hour, storedHead: ciDefaultHead, ciStatus: domain.CIStatusFailing,
			wantStatus: CheckNowNotFound, wantNextPollIn: time.Hour,
		},
		{
			name: "new head keeps the replaced due time and restarts the count", requestKind: ReactionKindCI,
			dueIn: 15 * time.Second, pendingAttempts: 6, storedHead: "sha-older", ciStatus: domain.CIStatusPending,
			wantStatus: CheckNowDue, wantFetches: 1, wantNextPollIn: 15 * time.Second, wantPendingAttempts: new(0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var (
				state   *State
				base    time.Time
				key     string
				fetches func() int
				pass    func()
			)
			if tt.review {
				state = stateWithReviewReaction(t, issueID, 42)
				base, key = reviewBaseTime, ReactionKey(issueID, ReactionKindReview)
				scm := &mockSCMAdapter{comments: []domain.ReviewComment{
					{ID: "300", Body: "new comment", SubmittedAt: base.Add(-10 * time.Second)},
				}}
				params := reviewParams(&reviewReconcileStore{}, scm, nil)
				fetches = func() int { return scm.calls }
				pass = func() {
					reconcileReviewComments(state, params, discardLogger(), context.Background(), newReviewMetricsSpy())
				}
				state.PendingReactions[key].Wait = waitDebounce
			} else {
				state = stateWithPendingReaction(t, issueID, "feature/fix", 1)
				base, key = ciBaseTime, ReactionKey(issueID, ReactionKindCI)
				ci := &mockCIProvider{result: domain.CIResult{Status: tt.ciStatus, FailingCount: 1}}
				params := ciParams(t, &ciReconcileStore{getFingerprintResult: tt.storedHead}, ci, &ciTrackerStub{}, defaultCISCM())
				fetches = func() int { return ci.calls }
				pass = func() {
					reconcileCIStatus(state, params, discardLogger(), context.Background(), newCIMetricsSpy())
				}
				state.ReactionAttempts[key] = tt.reactionAttempts
			}
			entry := state.PendingReactions[key]
			entry.PendingRetryAt = base.Add(tt.dueIn)
			entry.PendingAttempts = tt.pendingAttempts
			o := budgetOrchestrator(state, budgetTickConfig(0), &stubStore{}, &candidateTrackerAdapter{mockTrackerAdapter: &mockTrackerAdapter{}})

			got := o.checkNowOnLoop(entry.Identifier, tt.requestKind, base)

			if got.Status != tt.wantStatus {
				t.Fatalf("checkNowOnLoop(%q, %q).Status = %d, want %d", entry.Identifier, tt.requestKind, got.Status, tt.wantStatus)
			}
			if got.Status == CheckNowDue && (got.Identifier != entry.Identifier || got.Kind != entry.Kind || got.PRNumber != pullRequestOf(entry).Number) {
				t.Errorf("checkNowOnLoop(%q, %q) = identifier %q, kind %q, PR %d, want %q, %q, %d",
					entry.Identifier, tt.requestKind, got.Identifier, got.Kind, got.PRNumber, entry.Identifier, entry.Kind, pullRequestOf(entry).Number)
			}
			if n := fetches(); n != 0 {
				t.Errorf("provider calls after checkNowOnLoop = %d, want 0", n)
			}
			if _, ok := state.RetryAttempts[issueID]; ok {
				t.Error("RetryAttempts has an entry after checkNowOnLoop, want none")
			}

			pass()
			state.TrackerOpsWg.Wait()

			if n := fetches(); n != tt.wantFetches {
				t.Errorf("provider calls after the pass = %d, want %d", n, tt.wantFetches)
			}
			retry, scheduled := state.RetryAttempts[issueID]
			if scheduled != tt.wantRetry {
				t.Errorf("RetryAttempts[%q] present = %t, want %t", issueID, scheduled, tt.wantRetry)
			}
			if scheduled && retry.ReactionKind != ReactionKindCI {
				t.Errorf("RetryAttempts[%q].ReactionKind = %q, want %q", issueID, retry.ReactionKind, ReactionKindCI)
			}
			after, pending := state.PendingReactions[key]
			if pending == tt.wantRetry {
				t.Fatalf("PendingReactions[%q] present = %t, want %t", key, pending, !tt.wantRetry)
			}
			if !pending {
				return
			}
			if escalated := !after.EscalatedAt.IsZero(); escalated != tt.wantEscalated {
				t.Errorf("PendingReactions[%q].EscalatedAt set = %t, want %t", key, escalated, tt.wantEscalated)
			}
			if tt.wantWait != "" && after.Wait != tt.wantWait {
				t.Errorf("PendingReactions[%q].Wait = %q, want %q", key, after.Wait, tt.wantWait)
			}
			if want := base.Add(tt.wantNextPollIn); tt.wantNextPollIn != 0 && !after.PendingRetryAt.Equal(want) {
				t.Errorf("PendingReactions[%q].PendingRetryAt = %v, want %v", key, after.PendingRetryAt, want)
			}
			if tt.wantPendingAttempts != nil && after.PendingAttempts != *tt.wantPendingAttempts {
				t.Errorf("PendingReactions[%q].PendingAttempts = %d, want %d", key, after.PendingAttempts, *tt.wantPendingAttempts)
			}
		})
	}
}
