package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/domain"
)

func captureServer(t *testing.T) (server *httptest.Server, body func() []byte) {
	t.Helper()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("captureServer: read body: %v", err)
		}
		captured = b
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []byte { return captured }
}

func makeNotification() domain.Notification {
	return domain.Notification{
		Envelope: domain.NotificationEnvelope{
			NotificationID: "notif-slack-001",
			Timestamp:      "2026-06-10T15:00:00Z",
			Source:         "slack-test-host",
			IssueID:        "issue-8",
			Identifier:     "PROJ-8",
			SessionID:      "sess-slack",
			Attempt:        new(1),
			Agent:          "claude-code",
		},
		Message: domain.NotificationMessage{
			Severity: "critical",
			Title:    "Blocker found",
			Body:     "Agent cannot proceed without operator input.",
			Category: "blocked",
		},
	}
}

func TestSlack_NewNotifier_MissingWebhookURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config map[string]any
	}{
		{name: "absent webhook_url key", config: map[string]any{}},
		{name: "empty string webhook_url", config: map[string]any{"webhook_url": ""}},
		{name: "whitespace only webhook_url", config: map[string]any{"webhook_url": "   "}},
		{name: "nil webhook_url value", config: map[string]any{"webhook_url": nil}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := newNotifier(tt.config)
			if err == nil {
				t.Fatalf("newNotifier(%v) = nil error, want constructor error for missing webhook_url", tt.config)
			}
		})
	}
}

func TestSlack_NewNotifier_WrongTypeVsAbsentWebhookURL(t *testing.T) {
	t.Parallel()

	t.Run("webhook_url wrong type", func(t *testing.T) {
		t.Parallel()

		_, err := newNotifier(map[string]any{"webhook_url": 4242})

		if err == nil {
			t.Fatal("newNotifier(webhook_url=4242) error = nil, want error")
		}
		if err.Error() != "slack notifier: webhook_url: expected string, got integer" {
			t.Errorf("newNotifier(webhook_url=4242) error = %q, want %q", err.Error(), "slack notifier: webhook_url: expected string, got integer")
		}
	})

	t.Run("webhook_url absent", func(t *testing.T) {
		t.Parallel()

		_, err := newNotifier(map[string]any{})

		if err == nil {
			t.Fatal("newNotifier({}) error = nil, want error")
		}
		if err.Error() != "slack notifier: webhook_url is required" {
			t.Errorf("newNotifier({}) error = %q, want %q", err.Error(), "slack notifier: webhook_url is required")
		}
	})
}

func TestSlack_NewNotifier_ValidWebhookURL(t *testing.T) {
	t.Parallel()

	srv, _ := captureServer(t)

	n, err := newNotifier(map[string]any{"webhook_url": srv.URL})
	if err != nil {
		t.Fatalf("newNotifier(valid webhook_url) = %v, want nil", err)
	}
	if n == nil {
		t.Fatal("newNotifier(valid webhook_url) returned nil notifier")
	}
}

func TestSlack_Send_PostsSlackShapedBody(t *testing.T) {
	t.Parallel()

	srv, getBody := captureServer(t)

	n, err := newNotifier(map[string]any{"webhook_url": srv.URL})
	if err != nil {
		t.Fatalf("newNotifier: %v", err)
	}

	notif := makeNotification()
	if err := n.Send(context.Background(), notif); err != nil {
		t.Fatalf("Send: %v", err)
	}

	raw := getBody()
	if len(raw) == 0 {
		t.Fatal("Send posted empty body, want non-empty JSON")
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Send body unmarshal: %v", err)
	}

	text, ok := m["text"].(string)
	if !ok || text == "" {
		t.Fatalf("body[\"text\"] = %v, want non-empty string", m["text"])
	}

	if !strings.Contains(text, notif.Message.Title) {
		t.Errorf("text %q does not contain title %q", text, notif.Message.Title)
	}
	if !strings.Contains(text, notif.Message.Body) {
		t.Errorf("text %q does not contain body %q", text, notif.Message.Body)
	}
	severityUpper := strings.ToUpper(notif.Message.Severity)
	if !strings.Contains(strings.ToUpper(text), severityUpper) {
		t.Errorf("text %q does not contain severity %q (case-insensitive)", text, notif.Message.Severity)
	}
}

func TestSlack_Send_ContextCancellationReturnsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	n, err := newNotifier(map[string]any{"webhook_url": srv.URL})
	if err != nil {
		t.Fatalf("newNotifier: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = n.Send(ctx, makeNotification())
	if err == nil {
		t.Fatal("Send(cancelled ctx) = nil, want error")
	}
}
