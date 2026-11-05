package telegram

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/sortie-ai/sortie/internal/domain"
)

const (
	testToken      = "123456:SECRET-TOKEN"
	operatorHeader = "[INFO] PROJ-1: session.started\nSortie session started."
	statementStart = operatorHeader + "\n\n"
	channel        = "@ops_channel"
)

type receivedRequest struct {
	method, path string
	body         []byte
}

func serve(t *testing.T, status int) (*httptest.Server, chan receivedRequest) {
	t.Helper()

	requests := make(chan receivedRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- receivedRequest{method: r.Method, path: r.URL.Path, body: body}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, requests
}

func TestNewNotifierAt_RejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  map[string]any
		wantErr string
	}{
		{"bot_token absent", map[string]any{"chat_id": "1"}, "telegram notifier: bot_token is required"},
		{"bot_token null", map[string]any{"bot_token": nil, "chat_id": "1"}, "telegram notifier: bot_token is required"},
		{"bot_token blank", map[string]any{"bot_token": "  ", "chat_id": "1"}, "telegram notifier: bot_token is required"},
		{"bot_token wrong type", map[string]any{"bot_token": 42, "chat_id": "1"}, "telegram notifier: bot_token: expected string, got integer"},
		{"bot_token checked first", map[string]any{}, "telegram notifier: bot_token is required"},
		{"chat_id absent", map[string]any{"bot_token": testToken}, "telegram notifier: chat_id is required"},
		{"chat_id null", map[string]any{"bot_token": testToken, "chat_id": nil}, "telegram notifier: chat_id is required"},
		{"chat_id blank", map[string]any{"bot_token": testToken, "chat_id": " \t"}, "telegram notifier: chat_id is required"},
		{"chat_id wrong type", map[string]any{"bot_token": testToken, "chat_id": true}, "telegram notifier: chat_id: expected string or integer, got boolean"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := newNotifierAt(tt.config, "http://127.0.0.1:1")

			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("newNotifierAt(%v) error = %v, want %q", tt.config, err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "SECRET-TOKEN") {
				t.Errorf("newNotifierAt(%v) error holds the token: %q", tt.config, err)
			}
		})
	}
}

func TestNotifier_Send(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		chatID    any
		statement string
		status    int
		wantChat  string
		wantText  string
		wantErr   bool
	}{
		{name: "integer id goes out as a number", chatID: -1001234567890, wantChat: "-1001234567890"},
		{name: "numeric string id goes out as a number", chatID: " -1001234567890 ", status: http.StatusNoContent, wantChat: "-1001234567890"},
		{name: "channel name goes out as a string", chatID: channel},
		{name: "401 returns an error without the token", status: http.StatusUnauthorized, wantErr: true},
		{name: "5000 ASCII characters are cut to 4096", statement: strings.Repeat("a", 5000), wantText: (statementStart + strings.Repeat("a", 5000))[:4096]},
		{name: "5000 Cyrillic characters are cut to 4096, not 2048", statement: strings.Repeat("ж", 5000), wantText: statementStart + strings.Repeat("ж", 4096-len(statementStart))},
		{name: "an astral rune past 4096 units is dropped whole", statement: strings.Repeat("a", 4095-len(statementStart)) + "😀tail", wantText: statementStart + strings.Repeat("a", 4095-len(statementStart))},
		{name: "exactly 4096 units stay unchanged", statement: strings.Repeat("a", 4096-len(statementStart)), wantText: statementStart + strings.Repeat("a", 4096-len(statementStart))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, requests := serve(t, cmp.Or(tt.status, http.StatusOK))
			n, err := newNotifierAt(map[string]any{"bot_token": testToken, "chat_id": cmp.Or(tt.chatID, channel)}, srv.URL)
			if err != nil {
				t.Fatalf("newNotifierAt: %v", err)
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
			if req.method != http.MethodPost || req.path != "/bot"+testToken+"/sendMessage" {
				t.Errorf("Send request = %s %s, want POST /bot%s/sendMessage", req.method, req.path, testToken)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(req.body, &fields); err != nil {
				t.Fatalf("unmarshal request body %q: %v", req.body, err)
			}
			wantChat := cmp.Or(tt.wantChat, `"`+channel+`"`)
			if len(fields) != 2 || string(fields["chat_id"]) != wantChat {
				t.Errorf("Send body = %s, want exactly chat_id=%s and text", req.body, wantChat)
			}
			var text string
			if err := json.Unmarshal(fields["text"], &text); err != nil {
				t.Fatalf("unmarshal text %q: %v", fields["text"], err)
			}
			if want := cmp.Or(tt.wantText, operatorHeader); text != want || len(utf16.Encode([]rune(text))) > 4096 {
				t.Errorf("Send text = %.60q (%d units), want %.60q (%d units)", text, len(utf16.Encode([]rune(text))), want, len(utf16.Encode([]rune(want))))
			}
			if sendErr == nil {
				return
			}
			var logged bytes.Buffer
			slog.New(slog.NewTextHandler(&logged, nil)).Error("send failed", slog.Any("error", sendErr))
			if strings.Contains(sendErr.Error()+logged.String(), "SECRET-TOKEN") {
				t.Errorf("Send(status %d) error or its log rendering holds the token: %q, %q", tt.status, sendErr, logged.String())
			}
		})
	}
}
