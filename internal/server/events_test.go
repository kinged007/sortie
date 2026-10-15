package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/orchestrator"
)

func openStream(t *testing.T, timing streamTiming, lastEventID string) (srv *Server, next func() (event, id string)) {
	t.Helper()
	srv = New(Params{SnapshotFn: fixedSnapshot(orchestrator.RuntimeSnapshotResult{}), RefreshFn: acceptingRefresh()})
	srv.timing = timing
	srv.httpServer.ReadTimeout, srv.httpServer.WriteTimeout = 150*time.Millisecond, 150*time.Millisecond
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.httpServer.Close() })
	if lastEventID == "current" {
		lastEventID = strconv.FormatUint(srv.changes.current(), 10)
	}
	ctx, cancel := context.WithCancel(t.Context())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ln.Addr().String()+"/api/v1/events", nil)
	req.Header.Set("Last-Event-ID", lastEventID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	lines := bufio.NewScanner(resp.Body)
	return srv, func() (event, id string) {
		defer time.AfterFunc(5*time.Second, cancel).Stop()
		var frame [4]string // id, event, data, and the empty line that ends the frame
		for i := range frame {
			if !lines.Scan() {
				if err := lines.Err(); err != nil {
					t.Errorf("read event stream: %v", err)
				}
				return "", ""
			}
			frame[i] = lines.Text()
		}
		return strings.TrimPrefix(frame[1], "event: "), strings.TrimPrefix(frame[0], "id: ")
	}
}

type stalledWriter struct {
	*httptest.ResponseRecorder
	entered, release chan struct{}
	once             sync.Once
}

func (w *stalledWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.ResponseRecorder.Write(p)
}

func (w *stalledWriter) SetWriteDeadline(time.Time) error { return nil }

func TestEvents_StalledClientDoesNotBlockOnStateChange(t *testing.T) {
	t.Parallel()
	srv := New(Params{SnapshotFn: fixedSnapshot(orchestrator.RuntimeSnapshotResult{}), RefreshFn: acceptingRefresh()})
	w := &stalledWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(w.release) })
	go srv.handleEvents(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events", nil))
	<-w.entered

	const burst = 1000
	before := srv.changes.current()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range burst {
			srv.OnStateChange()
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%d OnStateChange calls did not return while a stream write was stalled", burst)
	}
	if got := srv.changes.current() - before; got != burst {
		t.Errorf("revision advance after %d OnStateChange calls = %d, want %d", burst, got, burst)
	}
}

func TestEvents_StateChangeReachesClient(t *testing.T) {
	t.Parallel()
	srv, next := openStream(t, streamTiming{heartbeat: time.Hour, signalInterval: 10 * time.Millisecond, frameDeadline: time.Second}, "")
	_, openingID := next()
	srv.OnStateChange()
	event, id := next()
	opening, _ := strconv.ParseUint(openingID, 10, 64)
	got, _ := strconv.ParseUint(id, 10, 64)
	if event != "change" || got <= opening {
		t.Errorf("frame after OnStateChange = {event %q, id %d}, want a change with id above %d", event, got, opening)
	}
}

func TestEvents_BurstCoalesces(t *testing.T) {
	t.Parallel()
	const interval = 300 * time.Millisecond
	start := time.Now() // before the stream opens, so the opening signal cannot be written earlier
	srv, next := openStream(t, streamTiming{heartbeat: time.Hour, signalInterval: interval, frameDeadline: time.Second}, "")
	next()
	for range 100 {
		srv.OnStateChange()
	}
	_, id := next()
	if elapsed, want := time.Since(start), strconv.FormatUint(srv.changes.current(), 10); id != want || elapsed < interval {
		t.Errorf("coalesced signal = id %q after %v, want id %q after at least %v", id, elapsed, want, interval)
	}
}

func TestEvents_ShutdownWithConnectedClient(t *testing.T) {
	t.Parallel()
	srv, next := openStream(t, streamTiming{heartbeat: time.Hour, signalInterval: 10 * time.Millisecond, frameDeadline: time.Second}, "")
	next()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() = %v, want nil", err)
	}
	if event, _ := next(); event != "" {
		t.Errorf("frame after Shutdown = %q, want the stream to have ended", event)
	}
}

func TestEvents_OutlivesWriteTimeout(t *testing.T) {
	t.Parallel()
	_, next := openStream(t, streamTiming{heartbeat: 20 * time.Millisecond, signalInterval: 10 * time.Millisecond, frameDeadline: time.Second}, "")
	next()
	for until := time.Now().Add(3 * 150 * time.Millisecond); time.Now().Before(until); {
		if event, _ := next(); event != "heartbeat" {
			t.Fatalf("frame event = %q, want %q", event, "heartbeat")
		}
	}
}

func TestEvents_Reconnect(t *testing.T) {
	t.Parallel()
	for lastEventID, wantEvents := range map[string][]string{"current": {"heartbeat", "heartbeat"}, "1": {"change", "heartbeat"}} {
		srv, next := openStream(t, streamTiming{heartbeat: 50 * time.Millisecond, signalInterval: 30 * time.Millisecond, frameDeadline: time.Second}, lastEventID)
		current := strconv.FormatUint(srv.changes.current(), 10)
		for i, wantEvent := range wantEvents {
			if event, id := next(); event != wantEvent || id != current {
				t.Errorf("Last-Event-ID %q: frame %d = {event %q, id %q}, want {event %q, id %q}", lastEventID, i, event, id, wantEvent, current)
			}
		}
	}
}
