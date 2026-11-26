package route_test

import (
	"context"
	"testing"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/notify/route"
)

func TestTrackerComment_SendPostsBodyThenStatement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentText string
		want      string
	}{
		{name: "statement follows the body after two line feeds", agentText: "Which one?", want: "body text\n\nWhich one?"},
		{name: "no statement posts the body alone", want: "body text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tracker := &commentRecorder{}
			router := route.NewRouter(tracker, nil)
			mustUpdate(t, router, route.Inputs{Entries: []config.NotificationBackend{entry(domain.TrackerCommentKind, domain.EventSessionStopped)}})
			notification := notificationOf(domain.EventSessionStopped)
			notification.Message.AgentText = tt.agentText

			router.Route(notification).Deliver(context.Background())

			if got := tracker.recorded(); len(got) != 1 || got[0] != (trackerComment{issueID: "ISS-1", text: tt.want}) {
				t.Errorf("tracker comments = %+v, want exactly one on ISS-1 with text %q", got, tt.want)
			}
		})
	}
}
