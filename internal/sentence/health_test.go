package sentence

import (
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/orchestrator"
)

func TestPollHealth(t *testing.T) {
	t.Parallel()
	ran := func(outcome string, failures int, lastSuccess time.Time) orchestrator.SnapshotPollHealth {
		return orchestrator.SnapshotPollHealth{Outcome: outcome, SnapshotHealthCheck: orchestrator.SnapshotHealthCheck{
			CheckedAt: outcomeNow, LastSuccessAt: lastSuccess, ConsecutiveFailures: failures}}
	}
	tests := []struct {
		name string
		poll orchestrator.SnapshotPollHealth
		want string
	}{
		{"not run", orchestrator.SnapshotPollHealth{}, "Sortie has not polled the tracker yet."},
		{"passing", ran("succeeded", 0, outcomeNow), "The latest poll read the tracker without an error."},
		{"one tracker failure", ran("tracker_failed", 1, outcomeNow.Add(-30*time.Second)),
			"The latest poll could not read the tracker; the last successful poll was 30s ago."},
		{"three preflight failures, no success", ran("preflight_failed", 3, time.Time{}),
			"3 polls in a row failed, and the latest stopped at the dispatch preflight before reading the tracker; no poll has succeeded since Sortie started."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := PollHealth(tt.poll, Context{Now: outcomeNow})

			if got != tt.want || got == fallback {
				t.Errorf("PollHealth(%+v) = %q, want %q", tt.poll, got, tt.want)
			}
		})
	}
}
