package sentence

import "github.com/sortie-ai/sortie/internal/orchestrator"

// pollOutcomePreflightFailed is the poll outcome of a tick that stopped at the
// dispatch preflight; every other failing outcome is a tracker read failure.
const pollOutcomePreflightFailed = "preflight_failed"

// PollHealth returns the sentence that words the poll check p, measuring
// relative times from c.Now. Safe for concurrent use; it never fails.
func PollHealth(p orchestrator.SnapshotPollHealth, c Context) string {
	if p.CheckedAt.IsZero() {
		return render("health_poll_unrun", "text", nil)
	}
	if p.ConsecutiveFailures == 0 {
		return render("health_poll_pass", "text", nil)
	}
	return render("health_poll_fail", "text", map[string]any{
		"Failures":   p.ConsecutiveFailures,
		"Preflight":  p.Outcome == pollOutcomePreflightFailed,
		"HasSuccess": !p.LastSuccessAt.IsZero(),
		"Ago":        secondsUntil(p.LastSuccessAt, c.Now),
	})
}
