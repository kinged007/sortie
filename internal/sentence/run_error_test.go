package sentence

import (
	"testing"

	"github.com/sortie-ai/sortie/internal/orchestrator"
	"github.com/sortie-ai/sortie/internal/persistence"
)

func TestRunError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stored string
		want   string
	}{
		{
			name:   "absence of work",
			stored: persistence.HandoffAbsenceErrorPrefix + "absence of work observed under observed policy",
			want:   "The agent finished without changing anything, so Sortie did not hand the issue off.",
		},
		{
			name:   "evidence not determinable",
			stored: persistence.HandoffAbsenceErrorPrefix + "evidence not determinable under strict policy",
			want:   "Sortie could not tell whether the agent changed anything, so it did not hand the issue off.",
		},
		{
			name:   "worker exit wrapper",
			stored: orchestrator.WorkerExitedErrorPrefix + persistence.HandoffAbsenceErrorPrefix + "absence of work observed under observed policy",
			want:   "The agent finished without changing anything, so Sortie did not hand the issue off.",
		},
		{
			name:   "multiline detail",
			stored: "agent turn 2: agent: port_exit: exit status 1\npanic: boom",
			want:   "The agent process ended unexpectedly: exit status 1\npanic: boom.",
		},
		{
			name:   "cancelled turn chain",
			stored: "agent turn 1: agent: turn_cancelled: turn cancelled: context canceled",
			want:   "Sortie stopped the session before the agent finished turn 1.",
		},
		{
			name:   "wrapped failed turn keeps the agent message",
			stored: orchestrator.WorkerExitedErrorPrefix + "agent turn 2: agent: turn_failed: the request failed: context deadline exceeded",
			want:   "The agent reported that turn 2 failed: the request failed.",
		},
		{
			name:   "session start failure keeps the cause",
			stored: `agent session start: agent: port_exit: failed to start subprocess: exec: "x": not found`,
			want:   `The agent process ended unexpectedly: failed to start subprocess: exec: "x": not found.`,
		},
		{
			name:   "cancelled worker stage",
			stored: "workspace preparation: prepare workspace for issue 7: context canceled",
			want:   "Sortie stopped the run while preparing the workspace.",
		},
		{
			name:   "legacy reason",
			stored: persistence.HandoffAbsenceErrorPrefix + "no output",
			want:   "Sortie did not hand the issue off: no output.",
		},
		{
			name:   "unprefixed error",
			stored: "worker error: agent crashed",
			want:   "worker error: agent crashed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := RunError(tt.stored); got != tt.want {
				t.Errorf("RunError(%q) = %q, want %q", tt.stored, got, tt.want)
			}
		})
	}
}
