// Package telegram implements [domain.Notifier] for the Telegram Bot API.
// It posts the operator rendering of a notification as a plain-text
// sendMessage request to one chat, group, or channel.
//
// The bot token is part of the request URL, so it appears only in the
// endpoint handed to [notifyhttp.NewClient], which keeps the URL out of
// every error it returns. The package imports no logger. A text longer
// than the Bot API limit is cut, never rejected or split.
package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/notify/notifyhttp"
	"github.com/sortie-ai/sortie/internal/notify/render"
	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/typeutil"
)

var _ domain.Notifier = (*notifier)(nil)

const (
	apiBaseURL = "https://api.telegram.org"

	// maxTextLength is the Bot API message limit in UTF-16 code units.
	maxTextLength = 4096
)

func init() {
	registry.Notifiers.Register("telegram", newNotifier)
}

// notifier is immutable after construction, so Send is safe for
// concurrent use.
type notifier struct {
	client *notifyhttp.Client
	chatID any
}

// sendMessageRequest is the whole JSON body.
type sendMessageRequest struct {
	ChatID any    `json:"chat_id"`
	Text   string `json:"text"`
}

// newNotifier constructs a telegram [domain.Notifier] from the entry's
// pass-through config.
func newNotifier(config map[string]any) (domain.Notifier, error) {
	return newNotifierAt(config, apiBaseURL)
}

// newNotifierAt constructs the notifier against baseURL, so a test can aim
// it at a local server. It requires bot_token and chat_id, makes no network
// call, and puts no field value in an error.
func newNotifierAt(config map[string]any, baseURL string) (domain.Notifier, error) {
	token, err := typeutil.RequiredString(config, "bot_token")
	if err != nil {
		return nil, fmt.Errorf("telegram notifier: %w", err)
	}
	chatID, err := chatIDFrom(config)
	if err != nil {
		return nil, fmt.Errorf("telegram notifier: %w", err)
	}
	return &notifier{
		client: notifyhttp.NewClient(baseURL + "/bot" + token + "/sendMessage"),
		chatID: chatID,
	}, nil
}

// chatIDFrom reads chat_id as a string or an integer. A value that parses
// as a base-10 int64 becomes that int64, which the Bot API types as an
// Integer; any other string, such as a channel's @username, stays a string.
func chatIDFrom(config map[string]any) (any, error) {
	raw := config["chat_id"]
	var text string
	switch v := raw.(type) {
	case nil:
	case string:
		text = v
	default:
		kind := typeutil.DescribeYAMLType(raw)
		if kind != "integer" {
			return nil, &typeutil.TypeFault{Key: "chat_id", Want: "string or integer", Got: kind}
		}
		text = fmt.Sprint(raw)
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("chat_id is required")
	}
	if id, err := strconv.ParseInt(text, 10, 64); err == nil {
		return id, nil
	}
	return text, nil
}

// Send posts the operator rendering, cut to the Bot API limit, and returns
// nil on any 2xx response. A non-2xx response or a transport failure yields
// a classified error that omits the token.
func (n *notifier) Send(ctx context.Context, notification domain.Notification) error {
	text := render.TruncateUTF16(render.Notification(notification).Operator, maxTextLength)
	return n.client.PostJSON(ctx, sendMessageRequest{ChatID: n.chatID, Text: text})
}
