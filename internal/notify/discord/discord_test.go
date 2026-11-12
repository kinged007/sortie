package discord

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/domain"
)

const operatorHeader = "[INFO] PROJ-1: session.started\nSortie session started."

type receivedRequest struct {
	method string
	body   []byte
}

func serve(t *testing.T, status int) (*httptest.Server, chan receivedRequest) {
	t.Helper()

	requests := make(chan receivedRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- receivedRequest{method: r.Method, body: body}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, requests
}

func TestNewNotifier_RejectsMissingWebhookURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config map[string]any
	}{
		{"webhook_url absent", map[string]any{}},
		{"webhook_url null", map[string]any{"webhook_url": nil}},
		{"webhook_url empty", map[string]any{"webhook_url": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := newNotifier(tt.config)

			const want = "discord notifier: webhook_url is required"
			if err == nil || err.Error() != want {
				t.Fatalf("newNotifier(%v) error = %v, want %q", tt.config, err, want)
			}
		})
	}
}

func TestNotifier_Send(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		statement   string
		wantContent string
		wantErr     bool
	}{
		{name: "204 posts the rendering", status: http.StatusNoContent, wantContent: operatorHeader},
		{name: "404 returns an error", status: http.StatusNotFound, wantContent: operatorHeader, wantErr: true},
		{name: "2100 character statement is cut to 2000", status: http.StatusNoContent, statement: strings.Repeat("a", 2100), wantContent: (operatorHeader + "\n\n" + strings.Repeat("a", 2100))[:2000]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, requests := serve(t, tt.status)
			n, err := newNotifier(map[string]any{"webhook_url": srv.URL})
			if err != nil {
				t.Fatalf("newNotifier: %v", err)
			}
			notification := domain.Notification{
				Envelope: domain.NotificationEnvelope{Identifier: "PROJ-1", EventType: domain.EventTypeSessionStarted},
				Message:  domain.NotificationMessage{Severity: "info", Facts: domain.SessionStartedFacts{}, AgentText: tt.statement},
			}

			sendErr := n.Send(context.Background(), notification)

			if (sendErr != nil) != tt.wantErr {
				t.Fatalf("Send(status %d) error = %v, want error %t", tt.status, sendErr, tt.wantErr)
			}
			req := <-requests
			if req.method != http.MethodPost {
				t.Errorf("Send request method = %q, want %q", req.method, http.MethodPost)
			}
			var got map[string]any
			if err := json.Unmarshal(req.body, &got); err != nil {
				t.Fatalf("unmarshal request body %q: %v", req.body, err)
			}
			want := map[string]any{
				"content":          tt.wantContent,
				"allowed_mentions": map[string]any{"parse": []any{}},
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Send body = %.80s, want %v", req.body, want)
			}
		})
	}
}
