package render_test

import (
	"testing"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/notify/render"
)

func TestNotification(t *testing.T) {
	t.Parallel()

	const (
		started      = "Sortie session started."
		advancedBody = "Sortie advanced the issue to the next stage.\nFrom: todo\nTo: review\nHop count: 2\nChain: chain-1"
		ceilingBody  = "Sortie did not advance the issue to the next stage.\nFrom: todo\nTo: review\n" +
			"Reason: the issue reached its limit of consecutive stage moves (dispatch.max_consecutive_hops)\nHop count: 3\nChain: chain-1"
	)
	advanced := domain.StageTransition{SourceRule: "todo", TargetRule: "review", ChainID: "chain-1", HopCount: 2}
	ceiling := domain.StageTransition{SourceRule: "todo", TargetRule: "review", ChainID: "chain-1", HopCount: 3, Reason: domain.StageReasonCeiling}

	tests := []struct {
		name string
		env  domain.NotificationEnvelope
		msg  domain.NotificationMessage
		want render.Text
	}{
		{
			name: "orchestrator event without a statement",
			env:  domain.NotificationEnvelope{Identifier: "PROJ-1", EventType: domain.EventTypeSessionStarted},
			msg:  domain.NotificationMessage{Severity: "info", Facts: domain.SessionStartedFacts{}},
			want: render.Text{Title: "PROJ-1: session.started", Body: started, Operator: "[INFO] PROJ-1: session.started\n" + started, Public: started},
		},
		{
			name: "statement with ampersand and angle brackets passes unchanged",
			env:  domain.NotificationEnvelope{Identifier: "7", DisplayID: "acme/repo#7", EventType: domain.EventTypeSessionStarted},
			msg:  domain.NotificationMessage{Severity: "info", Facts: domain.SessionStartedFacts{}, AgentText: "a & b <c> d"},
			want: render.Text{
				Title: "acme/repo#7: session.started", Body: started, AgentText: "a & b <c> d",
				Operator: "[INFO] acme/repo#7: session.started\n" + started + "\n\na & b <c> d", Public: started + "\n\na & b <c> d",
			},
		},
		{
			name: "agent message keeps the agent title and body",
			env:  domain.NotificationEnvelope{Identifier: "PROJ-8", EventType: domain.EventAgentMessage},
			msg:  domain.NotificationMessage{Severity: "critical", Title: "Blocker found", Body: "Needs input.", AgentText: "Which one?"},
			want: render.Text{
				Title: "Blocker found", Body: "Needs input.", AgentText: "Which one?",
				Operator: "[CRITICAL] Blocker found\nNeeds input.\n\nWhich one?", Public: "Needs input.\n\nWhich one?",
			},
		},
		{
			name: "stage advanced",
			env:  domain.NotificationEnvelope{IssueID: "id-9", EventType: domain.EventStageAdvanced, Stage: advanced},
			msg:  domain.NotificationMessage{Severity: "info", Facts: domain.StageFacts{}},
			want: render.Text{Title: "id-9: stage.advanced", Body: advancedBody, Operator: "[INFO] id-9: stage.advanced\n" + advancedBody, Public: advancedBody},
		},
		{
			name: "stage not advanced at the ceiling",
			env:  domain.NotificationEnvelope{Identifier: "PROJ-1", EventType: domain.EventStageNotAdvanced, Stage: ceiling},
			msg:  domain.NotificationMessage{Severity: "warning", Facts: domain.StageFacts{}},
			want: render.Text{Title: "PROJ-1: stage.not_advanced", Body: ceilingBody, Operator: "[WARNING] PROJ-1: stage.not_advanced\n" + ceilingBody, Public: ceilingBody},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := render.Notification(domain.Notification{Envelope: tt.env, Message: tt.msg})

			if got != tt.want {
				t.Errorf("Notification(%s) = %+v, want %+v", tt.name, got, tt.want)
			}
		})
	}
}
