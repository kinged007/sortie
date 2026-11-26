package mcpserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	httpPath = "/mcp"

	// Loopback binding makes a slow header sender a local-only nuisance, but
	// an unbounded header read would still pin a connection goroutine forever.
	httpReadHeaderTimeout = 10 * time.Second
)

// HTTPEvent is one JSON object per output line in HTTP mode. Listening and
// Connected are written once, the latter ahead of the first successful answer
// to an authorized request. Refused and Accept are written once for the first
// authorized POST whose Accept header cannot name both media types the SDK
// requires; Accept carries that header's value.
type HTTPEvent struct {
	Listening string `json:"listening,omitempty"`
	Connected bool   `json:"connected,omitempty"`
	Refused   string `json:"refused,omitempty"`
	Accept    string `json:"accept,omitempty"`
}

const refusedAccept = "accept"

// HTTPOptions configures [Server.ServeStreamableHTTP].
type HTTPOptions struct {
	// Bearer is the token every request presents as
	// "Authorization: Bearer <Bearer>"; a request without it is rejected. It is
	// required and never logged.
	Bearer string

	// Events receives one [HTTPEvent] JSON line per observation. A nil
	// Events discards them.
	Events io.Writer
}

// ServeStreamableHTTP serves the MCP Streamable HTTP transport on ln until
// ctx ends, then closes ln and returns nil. The caller supplies a loopback
// listener; the server never binds one itself.
//
// An empty opts.Bearer is refused with an error, and ln is closed. A failure
// of the listener before ctx ends is returned.
func (s *Server) ServeStreamableHTTP(ctx context.Context, ln net.Listener, opts HTTPOptions) error {
	if opts.Bearer == "" {
		_ = ln.Close()
		return errors.New("serve streamable http: bearer is required")
	}

	sdkHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s.sdk },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	srv := &http.Server{
		Handler: &pipeline{
			sdk:    sdkHandler,
			bearer: []byte("Bearer " + opts.Bearer),
			events: opts.Events,
			logger: s.logger,
		},
		ReadHeaderTimeout: httpReadHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		_ = srv.Close()
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve streamable http: %w", err)
		}
		return nil
	case err := <-serveErr:
		_ = srv.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve streamable http: %w", err)
	}
}

// pipeline applies the path, origin and bearer rules in front of the SDK
// handler.
type pipeline struct {
	sdk    http.Handler
	bearer []byte // full Authorization header value
	events io.Writer
	logger *slog.Logger

	mu        sync.Mutex // guards connected, refused and event writes
	connected bool
	refused   bool
}

func (p *pipeline) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != httpPath {
		http.NotFound(w, r)
		return
	}
	if !originAllowed(r.Header.Values("Origin")) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), p.bearer) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodPost {
		p.noteAccept(r.Header.Values("Accept"))
	}
	p.sdk.ServeHTTP(&connectedWriter{ResponseWriter: w, onSuccess: p.markConnected}, r)
}

func (p *pipeline) markConnected() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.connected {
		return
	}
	p.connected = true
	p.writeEvent(HTTPEvent{Connected: true})
}

func (p *pipeline) noteAccept(values []string) {
	if acceptsJSONAndStream(values) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refused {
		return
	}
	p.refused = true
	p.writeEvent(HTTPEvent{Refused: refusedAccept, Accept: strings.Join(values, ", ")})
}

// writeEvent must be called with p.mu held.
func (p *pipeline) writeEvent(event HTTPEvent) {
	if p.events == nil {
		return
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		p.logger.Error("failed to marshal HTTP event", slog.Any("error", err))
		return
	}
	if _, err := p.events.Write(append(encoded, '\n')); err != nil {
		p.logger.Error("failed to write HTTP event", slog.Any("error", err))
	}
}

type connectedWriter struct {
	http.ResponseWriter
	onSuccess func()
	wrote     bool
}

func (w *connectedWriter) WriteHeader(code int) {
	if !w.wrote && code >= http.StatusOK {
		w.wrote = true
		if code < http.StatusMultipleChoices {
			w.onSuccess()
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *connectedWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer after settling the status, because
// a flush with no status written commits an implicit 200.
func (w *connectedWriter) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// acceptsJSONAndStream mirrors the media-type test the SDK applies to a POST:
// both application/json and text/event-stream must be named, directly or by a
// wildcard.
func acceptsJSONAndStream(values []string) bool {
	var jsonOK, streamOK bool
	for _, value := range values {
		for token := range strings.SplitSeq(value, ",") {
			base, _, _ := strings.Cut(token, ";")
			switch strings.ToLower(strings.TrimSpace(base)) {
			case "application/json", "application/*":
				jsonOK = true
			case "text/event-stream", "text/*":
				streamOK = true
			case "*/*":
				jsonOK, streamOK = true, true
			}
		}
	}
	return jsonOK && streamOK
}

// originAllowed accepts a request without an Origin header, as a non-browser
// client sends none, and otherwise requires every Origin to name a loopback
// host so a web page cannot reach the server through the user's browser.
func originAllowed(origins []string) bool {
	return !slices.ContainsFunc(origins, func(origin string) bool {
		return !loopbackOrigin(origin)
	})
}

func loopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}
