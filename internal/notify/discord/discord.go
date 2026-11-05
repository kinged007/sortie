// Package discord implements [domain.Notifier] for a Discord channel
// webhook. It posts the operator rendering of a notification as the message
// content, with every mention turned off.
//
// The webhook token is part of the URL, so it appears only in the endpoint
// handed to [notifyhttp.NewClient], which keeps the URL out of every error
// it returns. The package imports no logger. A text longer than the content
// limit is cut, never rejected or split.
package discord

import (
	"context"
	"fmt"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/notify/notifyhttp"
	"github.com/sortie-ai/sortie/internal/notify/render"
	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/typeutil"
)

var _ domain.Notifier = (*notifier)(nil)

// maxContentLength is the content limit in UTF-16 code units. Discord names
// no unit, and a UTF-16 count cut at a rune boundary is accepted whether
// Discord counts code points or UTF-16 code units.
const maxContentLength = 2000

func init() {
	registry.Notifiers.Register("discord", newNotifier)
}

// notifier is immutable after construction, so Send is safe for
// concurrent use.
type notifier struct {
	client *notifyhttp.Client
}

// executeWebhookRequest is the whole JSON body.
type executeWebhookRequest struct {
	Content         string          `json:"content"`
	AllowedMentions allowedMentions `json:"allowed_mentions"`
}

type allowedMentions struct {
	// Parse must be a non-nil empty slice: nil encodes as null, which does
	// not turn mention parsing off.
	Parse []string `json:"parse"`
}

// newNotifier constructs a discord [domain.Notifier] from the entry's
// pass-through config. It requires a non-empty webhook_url, makes no
// network call, and puts no field value in an error.
func newNotifier(config map[string]any) (domain.Notifier, error) {
	endpoint, err := typeutil.RequiredString(config, "webhook_url")
	if err != nil {
		return nil, fmt.Errorf("discord notifier: %w", err)
	}
	return &notifier{client: notifyhttp.NewClient(endpoint)}, nil
}

// Send posts the operator rendering, cut to the content limit, and returns
// nil on any 2xx response. A non-2xx response or a transport failure yields
// a classified error that omits the URL.
func (n *notifier) Send(ctx context.Context, notification domain.Notification) error {
	content := render.TruncateUTF16(render.Notification(notification).Operator, maxContentLength)
	return n.client.PostJSON(ctx, executeWebhookRequest{
		Content:         content,
		AllowedMentions: allowedMentions{Parse: []string{}},
	})
}
