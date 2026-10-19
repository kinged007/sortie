package server

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
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
	subs, err := parseRunSubscriptions(r.URL.Query()["run"], r.Header.Get("Last-Event-ID"))
	if err != nil {
		writeErrorJSON(w, s.logger, http.StatusBadRequest, "invalid_parameter", err.Error())
		return
	}
	started := time.Now()
	reason := s.streamEvents(w, r, subs)
	s.logger.Debug("event stream closed",
		slog.String("reason", reason),
		slog.Duration("duration", time.Since(started)),
	)
}

// eventStream is the state of one open stream. Only the handler goroutine
// that owns it reads or writes it.
type eventStream struct {
	s            *Server
	r            *http.Request
	out          frameWriter
	subs         []runSubscription
	revision     string
	lastSignalAt time.Time
	hold         *time.Timer // set while a due change signal waits for its second
}

func (st *eventStream) currentRevision() string {
	return strconv.FormatUint(st.s.changes.current(), 10)
}

func (st *eventStream) changeDue() bool {
	return st.currentRevision() != st.revision && !time.Now().Before(st.lastSignalAt.Add(st.s.timing.signalInterval))
}

func (st *eventStream) writeChange() error {
	revision := st.currentRevision()
	if err := st.out.write(changeFrame(frameID(revision, st.subs))); err != nil {
		return err
	}
	st.revision = revision
	st.lastSignalAt = time.Now()
	st.disarmHold()
	return nil
}

func (st *eventStream) armHold() {
	st.hold = time.NewTimer(time.Until(st.lastSignalAt.Add(st.s.timing.signalInterval)))
}

func (st *eventStream) disarmHold() {
	if st.hold != nil {
		st.hold.Stop()
		st.hold = nil
	}
}

// holdFired returns a channel that receives when the armed hold ends, and
// blocks forever while none is armed.
func (st *eventStream) holdFired() <-chan time.Time {
	if st.hold == nil {
		return nil
	}
	return st.hold.C
}

// reopenFailedReads makes every subscription whose stored read failed open
// again and reports whether there was one.
func (st *eventStream) reopenFailedReads() bool {
	reopened := false
	for i := range st.subs {
		if st.subs[i].state == runReadFailed {
			st.subs[i].state = runOpen
			reopened = true
		}
	}
	return reopened
}

// streamEvents serves one stream until it ends and returns why it ended.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request, subs []runSubscription) string {
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

	st := &eventStream{s: s, r: r, out: out, subs: subs}
	defer st.disarmHold()
	// A revision part that is not the current revision is dropped rather
	// than kept, so no client-supplied byte is ever echoed back in a frame id.
	if revision, _, _ := strings.Cut(r.Header.Get("Last-Event-ID"), ";"); revision == st.currentRevision() {
		st.revision = revision
	}
	if st.currentRevision() != st.revision {
		if err := st.writeChange(); err != nil {
			return streamEndWriteFailed
		}
	}
	if reason := st.deliver(); reason != "" {
		return reason
	}

	ctx := r.Context()
	heartbeat := time.NewTicker(s.timing.heartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return streamEndClientGone
		case <-s.changes.closed():
			return streamEndShutdown
		case <-heartbeat.C:
			if err := out.write(heartbeatFrame(frameID(st.revision, st.subs))); err != nil {
				return streamEndWriteFailed
			}
			if st.reopenFailedReads() {
				if reason := st.deliver(); reason != "" {
					return reason
				}
			}
		case <-st.holdFired():
			st.hold = nil
			if st.currentRevision() != st.revision {
				if err := st.writeChange(); err != nil {
					return streamEndWriteFailed
				}
			}
		case <-pending:
			if reason := st.deliver(); reason != "" {
				return reason
			}
			if st.currentRevision() == st.revision || st.hold != nil {
				continue
			}
			if !st.changeDue() {
				st.armHold()
				continue
			}
			if err := st.writeChange(); err != nil {
				return streamEndWriteFailed
			}
		}
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

// put writes one frame under a fresh deadline and leaves it buffered. The
// deadline stays set until the next write or [frameWriter.flush] replaces it.
func (fw frameWriter) put(frame []byte) error {
	if err := fw.rc.SetWriteDeadline(time.Now().Add(fw.deadline)); err != nil {
		return err
	}
	_, err := fw.w.Write(frame)
	return err
}

// flush sends what put left buffered under a fresh deadline, then clears it.
func (fw frameWriter) flush() error {
	if err := fw.rc.SetWriteDeadline(time.Now().Add(fw.deadline)); err != nil {
		return err
	}
	if err := fw.rc.Flush(); err != nil {
		return err
	}
	return fw.rc.SetWriteDeadline(time.Time{})
}
