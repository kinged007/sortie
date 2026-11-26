package route

import (
	"context"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/notify/render"
)

// trackerComment posts an event's public rendering as a comment on the
// issue its envelope names.
type trackerComment struct {
	adapter domain.TrackerAdapter
}

func (c trackerComment) Send(ctx context.Context, n domain.Notification) error {
	return c.adapter.CommentIssue(ctx, n.Envelope.IssueID, render.Notification(n).Public)
}
