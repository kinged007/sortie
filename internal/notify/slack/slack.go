// Package slack implements [domain.Notifier] for a Slack incoming
// webhook. A Slack incoming webhook is itself an HTTP POST of JSON, so
// this backend differs from the generic webhook backend only in its wire
// format: it posts a Slack-shaped object with a text field.
package slack

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

func init() {
	registry.Notifiers.Register("slack", newNotifier)
}

// notifier posts notifications to a configured Slack incoming webhook.
type notifier struct {
	client *notifyhttp.Client
}

// newNotifier constructs a slack [domain.Notifier] from the entry's
// pass-through config. It requires a non-empty webhook_url; an absent or
// empty value (including a secret reference that resolved to the empty
// string) is rejected.
func newNotifier(config map[string]any) (domain.Notifier, error) {
	endpoint, err := typeutil.RequiredString(config, "webhook_url")
	if err != nil {
		return nil, fmt.Errorf("slack notifier: %w", err)
	}
	client := notifyhttp.NewClient(endpoint)
	return &notifier{client: client}, nil
}

type slackPayload struct {
	Text string `json:"text"`
}

// Send posts a Slack-shaped body and returns nil on any 2xx response.
// A non-2xx response or a transport failure yields a classified error
// that omits the URL, request body, and response body.
func (n *notifier) Send(ctx context.Context, notification domain.Notification) error {
	return n.client.PostJSON(ctx, slackPayload{Text: render.Notification(notification).Operator})
}
