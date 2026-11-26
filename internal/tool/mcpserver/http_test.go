package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sortie-ai/sortie/internal/waittest"
)

const (
	testBearer     = "s3cret-token"
	connectedEvent = `{"connected":true}` + "\n"
	listToolsBody  = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
)

type lockedBuffer struct {
	sync.Mutex
	strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Builder.Write(p)
}

func (b *lockedBuffer) String() string {
	b.Lock()
	defer b.Unlock()
	return b.Builder.String()
}

func startHTTP(t *testing.T) (baseURL string, events *lockedBuffer) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	events = &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- NewServer(testRegistry(), nil, "1.0.0").ServeStreamableHTTP(waittest.Context(t), ln, HTTPOptions{Bearer: testBearer, Events: events})
	}()
	t.Cleanup(func() {
		if err := waittest.Receive(t, done, "ServeStreamableHTTP to return"); err != nil {
			t.Errorf("ServeStreamableHTTP() = %v, want nil", err)
		}
	})
	return "http://" + ln.Addr().String(), events
}

func post(t *testing.T, baseURL string, headers map[string]string, body string) (status int, header http.Header, got []byte) {
	t.Helper()
	req, _ := http.NewRequestWithContext(waittest.Context(t), http.MethodPost, baseURL+"/mcp", strings.NewReader(body))
	req.Host = headers["Host"]
	all := map[string]string{"Authorization": "Bearer " + testBearer, "Accept": "application/json, text/event-stream", "Content-Type": "application/json"}
	maps.Copy(all, headers)
	for k, v := range all {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, _ = io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, got
}

func httpSend(baseURL string) sendFunc {
	id := 0
	return func(t *testing.T, method string, params map[string]any) json.RawMessage {
		t.Helper()
		id++
		headers := map[string]string{}
		if _, modern := params["_meta"]; modern {
			headers["Mcp-Protocol-Version"] = modernVersion
			headers["Mcp-Method"] = method
			headers["Mcp-Name"], _ = params["name"].(string)
		}
		_, _, got := post(t, baseURL, headers, string(rpcRequest(id, method, params)))
		return rpcResult(t, method, got)
	}
}

type spyResponseWriter struct {
	http.ResponseWriter
	log io.Writer
}

func (w spyResponseWriter) WriteHeader(int) { _, _ = io.WriteString(w.log, "status\n") }

func TestServeStreamableHTTP_RequestRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		headers    map[string]string
		wantStatus int
		wantAuth   string
		wantEvent  string
	}{
		{name: "missing bearer", headers: map[string]string{"Authorization": ""}, wantStatus: http.StatusUnauthorized, wantAuth: "Bearer"},
		{name: "wrong bearer", headers: map[string]string{"Authorization": "Bearer nope"}, wantStatus: http.StatusUnauthorized, wantAuth: "Bearer"},
		{name: "foreign origin", headers: map[string]string{"Origin": "https://evil.example"}, wantStatus: http.StatusForbidden},
		{name: "loopback host with another port", headers: map[string]string{"Host": "127.0.0.1:1"}, wantStatus: http.StatusOK, wantEvent: connectedEvent},
		{name: "accept lacking a media type", headers: map[string]string{"Accept": "application/json"}, wantStatus: http.StatusBadRequest, wantEvent: `{"refused":"accept","accept":"application/json"}` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			baseURL, events := startHTTP(t)

			status, header, _ := post(t, baseURL, tt.headers, listToolsBody)

			got := fmt.Sprintf("%d %q %q", status, header.Get("WWW-Authenticate"), events.String())
			if want := fmt.Sprintf("%d %q %q", tt.wantStatus, tt.wantAuth, tt.wantEvent); got != want {
				t.Errorf("POST (%s) = (status, WWW-Authenticate, events) %s, want %s", tt.name, got, want)
			}
		})
	}
}

func TestPipeline_ConnectedEventPrecedesFirstAnswer(t *testing.T) {
	t.Parallel()

	log := &lockedBuffer{}
	srv := NewServer(testRegistry(), nil, "1.0.0")
	p := &pipeline{
		sdk: mcp.NewStreamableHTTPHandler(
			func(*http.Request) *mcp.Server { return srv.sdk },
			&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
		),
		bearer: []byte("Bearer " + testBearer),
		events: log,
		logger: srv.logger,
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(listToolsBody))
	req.Header.Set("Authorization", "Bearer "+testBearer)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")

	p.ServeHTTP(spyResponseWriter{httptest.NewRecorder(), log}, req)

	if got, want := log.String(), connectedEvent+"status\n"; !strings.HasPrefix(got, want) {
		t.Errorf("ServeHTTP(tools/list) wrote %q, want it to begin with %q", got, want)
	}
}

func TestServeStreamableHTTP_ResultsMatchStdio(t *testing.T) {
	t.Parallel()

	for _, s := range sessions {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()

			baseURL, events := startHTTP(t)
			overHTTP, overStdio := s, s
			overHTTP.send, overStdio.send = httpSend(baseURL), startStdio(t)
			overHTTP.open(t)
			overStdio.open(t)

			for method, params := range map[string]map[string]any{"tools/list": nil, "tools/call": {"name": "echo", "arguments": map[string]any{"input": "hello"}}} {
				if got, want := overHTTP.call(t, method, params), overStdio.call(t, method, params); !bytes.Equal(got, want) {
					t.Errorf("HTTP %s result = %s, want stdio result %s", method, got, want)
				}
			}

			if got := events.String(); got != connectedEvent {
				t.Errorf("events after three HTTP requests = %q, want %q", got, connectedEvent)
			}
		})
	}
}
