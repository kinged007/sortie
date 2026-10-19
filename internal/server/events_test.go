package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/orchestrator"
)

const (
	runA             = "RUNA"
	runB             = "RUNB"
	socketBufferSize = 32 << 10
)

var runStreamTiming = streamTiming{heartbeat: time.Hour, signalInterval: 10 * time.Millisecond, frameDeadline: time.Second}

type frameReader func() (event, id, data string)

type runSource struct {
	mu     sync.Mutex
	events map[string][]orchestrator.RunEvent
}

func (s *runSource) add(dispatchID string, events ...orchestrator.RunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.events == nil {
		s.events = map[string][]orchestrator.RunEvent{}
	}
	s.events[dispatchID] = append(s.events[dispatchID], events...)
}

func (s *runSource) stop(dispatchID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.events, dispatchID)
}

func (s *runSource) after(dispatchID string, cursor int64) (orchestrator.RunEventPage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, running := s.events[dispatchID]
	return orchestrator.PageAfter(events, cursor), running
}

func bulkEvents(first, last int64) []orchestrator.RunEvent {
	events := liveRunEvents(first, last)
	for i := range events {
		events[i].Message = strings.Repeat("x", 1000)
	}
	return events
}

type smallBufferListener struct{ net.Listener }

func (l smallBufferListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return conn, conn.(*net.TCPConn).SetWriteBuffer(socketBufferSize)
}

func dialSmallBuffer(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return conn, conn.(*net.TCPConn).SetReadBuffer(socketBufferSize)
}

type pacedReader struct{ io.Reader }

func (p pacedReader) Read(b []byte) (int, error) {
	n, err := p.Reader.Read(b[:min(len(b), 4096)])
	time.Sleep(time.Duration(n) * 10 * time.Millisecond / 4096)
	return n, err
}

func serveStreams(t *testing.T, params Params, timing streamTiming) (srv *Server, closed <-chan struct{}) {
	t.Helper()
	params.SnapshotFn, params.RefreshFn = fixedSnapshot(orchestrator.RuntimeSnapshotResult{}), acceptingRefresh()
	srv = New(params)
	srv.timing = timing
	srv.httpServer.ReadTimeout, srv.httpServer.WriteTimeout = 150*time.Millisecond, 150*time.Millisecond
	connClosed := make(chan struct{}, 4)
	srv.httpServer.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			connClosed <- struct{}{}
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv.httpServer.Addr = ln.Addr().String()
	go func() { _ = srv.Serve(smallBufferListener{ln}) }()
	t.Cleanup(func() { _ = srv.httpServer.Close() })
	return srv, connClosed
}

func streamFrames(t *testing.T, srv *Server, lastEventID string, paced bool, runs ...string) frameReader {
	t.Helper()
	if rest, ok := strings.CutPrefix(lastEventID, "current"); ok {
		lastEventID = strconv.FormatUint(srv.changes.current(), 10) + rest
	}
	ctx, cancel := context.WithCancel(t.Context())
	target := "http://" + srv.httpServer.Addr + "/api/v1/events?" + url.Values{"run": runs}.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	req.Header.Set("Last-Event-ID", lastEventID)
	resp, err := (&http.Client{Transport: &http.Transport{DialContext: dialSmallBuffer}}).Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	var body io.Reader = resp.Body
	if paced {
		body = pacedReader{body}
	}
	lines := bufio.NewScanner(body)
	return func() (event, id, data string) {
		defer time.AfterFunc(5*time.Second, cancel).Stop()
		for _, line := range []*string{&id, &event, &data, new(string)} {
			if !lines.Scan() {
				if err := lines.Err(); err != nil {
					t.Errorf("read event stream: %v", err)
				}
				return "", "", ""
			}
			*line = lines.Text()
		}
		return strings.TrimPrefix(event, "event: "), strings.TrimPrefix(id, "id: "), strings.TrimPrefix(data, "data: ")
	}
}

func openStream(t *testing.T, timing streamTiming, lastEventID string) (srv *Server, next func() (event, id string)) {
	t.Helper()
	srv, _ = serveStreams(t, Params{}, timing)
	frames := streamFrames(t, srv, lastEventID, false)
	return srv, func() (event, id string) {
		event, id, _ = frames()
		return event, id
	}
}

func openCatchUp(t *testing.T, lastEventID string, events int64, runs ...string) (*Server, frameReader) {
	t.Helper()
	src := &runSource{}
	for _, run := range runs {
		src.add(run, bulkEvents(1, events)...)
	}
	srv, _ := serveStreams(t, Params{RunEventsAfterFn: src.after}, runStreamTiming)
	return srv, streamFrames(t, srv, lastEventID, true, runs...)
}

func readThrough(t *testing.T, next frameReader, size int) (events []string) {
	t.Helper()
	for received := 0; received < size; {
		event, _, data := next()
		if event == "" {
			t.Fatalf("stream ended after %d of %d bytes", received, size)
		}
		events, received = append(events, event), received+len(data)
	}
	return events
}

func runFrames(next frameReader, count int) []string {
	var frames []string
	for len(frames) < count {
		event, id, data := next()
		if event == "" {
			break
		}
		if event == "change" {
			continue
		}
		_, cursors, _ := strings.Cut(id, ";")
		frame := strings.TrimSpace(event + " " + cursors)
		if event != "agent_event" {
			frame += " " + data
		}
		frames = append(frames, frame)
	}
	return frames
}

func agentFrames(run string, first, last int64) []string {
	var frames []string
	for seq := first; seq <= last; seq++ {
		frames = append(frames, fmt.Sprintf("agent_event %s:%d", run, seq))
	}
	return frames
}

func assertFrames(t *testing.T, got, want []string) {
	t.Helper()
	i := 0
	for i < min(len(got), len(want)) && got[i] == want[i] {
		i++
	}
	if len(got) != len(want) || i < len(want) {
		t.Fatalf("run frames diverge at index %d: got %q of %d, want %q of %d", i, got[i:min(i+1, len(got))], len(got), want[i:min(i+1, len(want))], len(want))
	}
}

func TestEvents_RunSubscriptionDeliversInOrderThenEnds(t *testing.T) {
	t.Parallel()
	src := &runSource{}
	src.add(runA, liveRunEvents(1, 5)...)
	history := &fakeRunHistory{events: map[string][]orchestrator.RunEvent{}}
	srv, _ := serveStreams(t, Params{RunEventsAfterFn: src.after, RunHistory: history}, runStreamTiming)
	next := streamFrames(t, srv, "", false, runA+":2")

	got := runFrames(next, 3)
	src.add(runA, liveRunEvents(6, 6)...)
	srv.OnStateChange()
	got = append(got, runFrames(next, 1)...)
	history.events[runA] = liveRunEvents(1, 6)
	src.stop(runA)
	srv.OnStateChange()
	got = append(got, runFrames(next, 1)...)

	assertFrames(t, got, append(agentFrames(runA, 3, 6), fmt.Sprintf(`run_ended %s:6 {"dispatch_id":%q,"last_seq":6}`, runA, runA)))
}

func TestEvents_RunSubscriptionResumesFromLastFrameID(t *testing.T) {
	t.Parallel()
	src := &runSource{}
	src.add(runA, liveRunEvents(1, 3)...)
	srv, _ := serveStreams(t, Params{RunEventsAfterFn: src.after}, runStreamTiming)
	first := streamFrames(t, srv, "", false, runA)
	var lastID string
	for seen := 0; seen < 3; {
		event, id, _ := first()
		if event == "agent_event" {
			seen++
		}
		lastID = id
	}
	src.add(runA, liveRunEvents(4, 6)...)

	second := streamFrames(t, srv, lastID, false, runA+":0")

	assertFrames(t, runFrames(second, 3), agentFrames(runA, 4, 6))
}

func TestEvents_RunSubscriptionResetsWhenEventsAreGone(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		retained []orchestrator.RunEvent
		run      string
		want     []string
	}{
		{
			name:     "cursor behind retention",
			retained: liveRunEvents(201, 1200),
			run:      runA + ":50",
			want: append([]string{fmt.Sprintf(`reset %s:200 {"dispatch_id":%q,"reason":"cursor_expired","first_seq":201}`, runA, runA)},
				agentFrames(runA, 201, 1200)...),
		},
		{
			name: "run not retained",
			run:  "NOSUCHRUN",
			want: []string{`reset {"dispatch_id":"NOSUCHRUN","reason":"not_retained"}`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := &runSource{}
			src.add(runA, tt.retained...)
			srv, _ := serveStreams(t, Params{RunEventsAfterFn: src.after, RunHistory: &fakeRunHistory{}}, runStreamTiming)

			next := streamFrames(t, srv, "", false, tt.run)

			assertFrames(t, runFrames(next, len(tt.want)), tt.want)
		})
	}
}

func TestEvents_NeverReadingClientDoesNotDelayEventLoop(t *testing.T) {
	t.Parallel()
	src := &runSource{}
	src.add(runA, bulkEvents(1, 1)...)
	srv, closed := serveStreams(t, Params{RunEventsAfterFn: src.after}, runStreamTiming)
	streamFrames(t, srv, "", false, runA)

	loopTime := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		for seq := int64(2); seq <= 10_000; seq++ {
			src.add(runA, bulkEvents(seq, seq)...)
			srv.OnStateChange()
		}
		loopTime <- time.Since(start)
	}()

	select {
	case took := <-loopTime:
		if took >= runStreamTiming.frameDeadline {
			t.Errorf("10000 appends with OnStateChange took %v, want under the %v frame deadline", took, runStreamTiming.frameDeadline)
		}
	case <-time.After(5 * runStreamTiming.frameDeadline):
		t.Fatal("10000 appends with OnStateChange did not finish while a stream write was stalled")
	}
	select {
	case <-closed:
	case <-time.After(runStreamTiming.frameDeadline + 2*time.Second):
		t.Fatal("stream of a client that never reads still open after the frame deadline")
	}
}

func TestEvents_ChangeSignalDuringCatchUp(t *testing.T) {
	t.Parallel()
	srv, next := openCatchUp(t, "current;"+runA+":0", 200, runA)
	opened := strconv.FormatUint(srv.changes.current(), 10)
	if slices.Contains(readThrough(t, next, 64<<10), "change") {
		t.Fatal("change frame at open for a Last-Event-ID carrying the current revision")
	}
	srv.OnStateChange()

	sawChange := false
	for {
		event, id, _ := next()
		revision, _, _ := strings.Cut(id, ";")
		if event == "" {
			t.Fatal("stream ended before the last agent_event")
		}
		sawChange = sawChange || event == "change"
		if event == "agent_event" && revision != opened && !sawChange {
			t.Fatalf("agent_event id %q carries the new revision before a change frame", id)
		}
		if event == "agent_event" && strings.HasSuffix(id, ":200") {
			break
		}
	}

	if !sawChange {
		t.Error("no change frame with the greater revision before the last agent_event")
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
	srv, next := openCatchUp(t, "", 1000, runA, runB)
	readThrough(t, next, 64<<10)

	start := time.Now()
	shutdownErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr <- srv.Shutdown(ctx)
	}()
	for event, _, _ := next(); event != ""; event, _, _ = next() {
	}

	if err, took := <-shutdownErr, time.Since(start); err != nil || took >= 2*runStreamTiming.frameDeadline {
		t.Errorf("Shutdown() = %v after %v, want nil within %v", err, took, 2*runStreamTiming.frameDeadline)
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
