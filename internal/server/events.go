package server

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	streamEndWriteFailed = "write_failed"
	streamEndClientGone  = "client_gone"
	streamEndShutdown    = "shutdown"
)

// changeFeed counts changes and wakes the open event streams.
type changeFeed struct {
	revision  atomic.Uint64              // seeded by newChangeFeed, raised by publish
	mu        sync.Mutex                 // guards streams; never held across I/O, a receive, or a wait
	streams   map[chan struct{}]struct{} // one capacity-1 pending flag per open stream
	done      chan struct{}              // closed once, when shutdown begins
	closeOnce sync.Once
}

// streamTiming holds the clocks of an event stream. [New] sets the
// production values; tests shorten them on their own server before serving.
type streamTiming struct {
	heartbeat      time.Duration
	signalInterval time.Duration
	frameDeadline  time.Duration
}

func newChangeFeed() *changeFeed {
	f := &changeFeed{
		streams: make(map[chan struct{}]struct{}),
		done:    make(chan struct{}),
	}
	f.revision.Store(uint64(time.Now().UnixNano()))
	return f
}

// publish records a change and wakes every open stream without blocking.
func (f *changeFeed) publish() {
	f.revision.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	for pending := range f.streams {
		select {
		case pending <- struct{}{}:
		default:
		}
	}
}

// subscribe registers a stream and returns its pending flag. The flag holds
// at most one change, so a stream that stops reading never slows publish.
func (f *changeFeed) subscribe() (pending <-chan struct{}, unsubscribe func()) {
	flag := make(chan struct{}, 1)
	f.mu.Lock()
	f.streams[flag] = struct{}{}
	f.mu.Unlock()
	return flag, func() {
		f.mu.Lock()
		delete(f.streams, flag)
		f.mu.Unlock()
	}
}

func (f *changeFeed) current() uint64 {
	return f.revision.Load()
}

// close ends every stream by closing the channel that [changeFeed.closed]
// returns. It is safe to call more than once.
func (f *changeFeed) close() {
	f.closeOnce.Do(func() { close(f.done) })
}

func (f *changeFeed) closed() <-chan struct{} {
	return f.done
}

func (f *changeFeed) isClosed() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

func changeFrame(id string) []byte {
	return []byte("id: " + id + "\nevent: change\ndata: {}\n\n")
}

// heartbeatFrame carries lastID because the empty line that ends any frame
// copies the browser's last-event-ID buffer into its cursor, and a frame
// without an id line would clear the cursor that the reconnect rule reads.
func heartbeatFrame(lastID string) []byte {
	return []byte("id: " + lastID + "\nevent: heartbeat\ndata: {}\n\n")
}

func (s *Server) routeEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.methodNotAllowed(w, r, http.MethodGet)
		return
	}
	s.handleEvents(w, r)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	reason := s.streamEvents(w, r)
	s.logger.Debug("event stream closed",
		slog.String("reason", reason),
		slog.Duration("duration", time.Since(started)),
	)
}

// streamEvents serves one stream until it ends and returns why it ended.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) string {
	pending, unsubscribe := s.changes.subscribe()
	defer unsubscribe()

	out := frameWriter{w: w, rc: http.NewResponseController(w), deadline: s.timing.frameDeadline}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := out.write(nil); err != nil {
		return streamEndWriteFailed
	}
	if s.changes.isClosed() {
		return streamEndShutdown
	}

	ctx := r.Context()
	// A Last-Event-ID that is not the current revision is dropped rather than
	// kept, so no client-supplied byte is ever echoed back in a heartbeat.
	lastID := ""
	if id := strconv.FormatUint(s.changes.current(), 10); id == r.Header.Get("Last-Event-ID") {
		lastID = id
	}
	var lastSignalAt time.Time

	signal := func() error {
		id := strconv.FormatUint(s.changes.current(), 10)
		if id == lastID {
			return nil
		}
		if err := out.write(changeFrame(id)); err != nil {
			return err
		}
		lastID = id
		lastSignalAt = time.Now()
		return nil
	}
	if err := signal(); err != nil {
		return streamEndWriteFailed
	}

	heartbeat := time.NewTicker(s.timing.heartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return streamEndClientGone
		case <-s.changes.closed():
			return streamEndShutdown
		case <-heartbeat.C:
			if err := out.write(heartbeatFrame(lastID)); err != nil {
				return streamEndWriteFailed
			}
		case <-pending:
			if !lastSignalAt.IsZero() {
				if reason := s.waitOut(ctx, time.Until(lastSignalAt.Add(s.timing.signalInterval))); reason != "" {
					return reason
				}
			}
			select {
			case <-pending:
			default:
			}
			if err := signal(); err != nil {
				return streamEndWriteFailed
			}
		}
	}
}

// waitOut sleeps for d and returns "" or, when the client leaves or shutdown
// begins first, the reason the stream ends.
func (s *Server) waitOut(ctx context.Context, d time.Duration) string {
	if d <= 0 {
		return ""
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ""
	case <-ctx.Done():
		return streamEndClientGone
	case <-s.changes.closed():
		return streamEndShutdown
	}
}

// frameWriter writes event-stream frames to one response.
type frameWriter struct {
	w        http.ResponseWriter
	rc       *http.ResponseController
	deadline time.Duration
}

// write bounds one write by the frame deadline and clears the deadline
// afterwards: a lapsed deadline is not extended, and an HTTP/2 stream that
// idles past one is reset. An empty frame only flushes the headers.
func (fw frameWriter) write(frame []byte) error {
	if err := fw.rc.SetWriteDeadline(time.Now().Add(fw.deadline)); err != nil {
		return err
	}
	if _, err := fw.w.Write(frame); err != nil {
		return err
	}
	if err := fw.rc.Flush(); err != nil {
		return err
	}
	return fw.rc.SetWriteDeadline(time.Time{})
}
