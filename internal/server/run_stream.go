package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/sortie-ai/sortie/internal/orchestrator"
)

const (
	maxRunSubscriptions = 16
	maxSeqDigits        = 19

	resetCursorExpired = "cursor_expired"
	resetNotRetained   = "not_retained"
)

type runSubscriptionState int

const (
	runOpen runSubscriptionState = iota
	runEnded
	runDropped
	// runReadFailed waits for the next heartbeat to read the stored copy again.
	runReadFailed
)

// runSubscription is one run a stream carries. Only the stream's goroutine
// reads or writes it.
type runSubscription struct {
	dispatchID string
	cursor     int64 // the run's seq in the frame id
	state      runSubscriptionState
}

// agentEventFrame is the data of an agent_event frame.
type agentEventFrame struct {
	dispatchID string
	event      runEventResponse
}

// MarshalJSON writes dispatch_id, then the members runEventResponse writes,
// in their order.
func (f agentEventFrame) MarshalJSON() ([]byte, error) {
	id, err := json.Marshal(f.dispatchID)
	if err != nil {
		return nil, err
	}
	event, err := f.event.MarshalJSON()
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(id)+len(event)+16)
	out = append(out, `{"dispatch_id":`...)
	out = append(out, id...)
	out = append(out, ',')
	return append(out, event[1:]...), nil
}

// runEndedFrame is the data of a run_ended frame.
type runEndedFrame struct {
	DispatchID string `json:"dispatch_id"`
	LastSeq    int64  `json:"last_seq"`
}

// runResetFrame is the data of a reset frame.
type runResetFrame struct {
	DispatchID string `json:"dispatch_id"`
	Reason     string `json:"reason"`
	FirstSeq   *int64 `json:"first_seq,omitempty"` // set only with cursor_expired
}

func runFrame(id, eventName string, data any) ([]byte, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return []byte("id: " + id + "\nevent: " + eventName + "\ndata: " + string(body) + "\n\n"), nil
}

// frameID returns the revision followed by the cursor of every subscription
// that is not dropped. It carries only the server's revision, dispatch IDs
// that matched the grammar, and integers, so no other client byte reaches
// the stream.
func frameID(revision string, subs []runSubscription) string {
	var id strings.Builder
	id.WriteString(revision)
	for _, sub := range subs {
		if sub.state == runDropped {
			continue
		}
		id.WriteByte(';')
		id.WriteString(sub.dispatchID)
		id.WriteByte(':')
		id.WriteString(strconv.FormatInt(sub.cursor, 10))
	}
	return id.String()
}

// parseRunSubscriptions resolves the run query values and the cursors of the
// Last-Event-ID header. An invalid query value is an error whose message
// echoes no input; the header never causes one.
func parseRunSubscriptions(runValues []string, lastEventID string) ([]runSubscription, error) {
	if len(runValues) > maxRunSubscriptions {
		return nil, errors.New("run may be given at most 16 times")
	}
	subs := make([]runSubscription, 0, len(runValues))
	for _, value := range runValues {
		dispatchID, seq, hasSeq := strings.Cut(value, ":")
		if !orchestrator.IsDispatchID(dispatchID) {
			return nil, errors.New("run must be a dispatch ID, optionally followed by a colon and a sequence number")
		}
		var cursor int64
		if hasSeq {
			var ok bool
			if cursor, ok = parseSeq(seq); !ok {
				return nil, errors.New("run must be a dispatch ID, optionally followed by a colon and a sequence number")
			}
		}
		if slices.ContainsFunc(subs, func(sub runSubscription) bool { return sub.dispatchID == dispatchID }) {
			return nil, errors.New("run must not name a dispatch ID twice")
		}
		subs = append(subs, runSubscription{dispatchID: dispatchID, cursor: cursor})
	}

	_, header, _ := strings.Cut(lastEventID, ";")
	resolved := make(map[string]bool, len(subs))
	for part := range strings.SplitSeq(header, ";") {
		dispatchID, seq, _ := strings.Cut(part, ":")
		cursor, ok := parseSeq(seq)
		i := slices.IndexFunc(subs, func(sub runSubscription) bool { return sub.dispatchID == dispatchID })
		if !ok || i < 0 || resolved[dispatchID] {
			continue
		}
		resolved[dispatchID] = true
		subs[i].cursor = cursor
	}
	return subs, nil
}

// parseSeq parses 1 to 19 decimal digits that fit an int64.
func parseSeq(s string) (int64, bool) {
	if len(s) == 0 || len(s) > maxSeqDigits {
		return 0, false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	seq, err := strconv.ParseInt(s, 10, 64)
	return seq, err == nil
}

// interrupted returns the reason the stream must end before its next frame,
// or "".
func (st *eventStream) interrupted() string {
	if st.s.changes.isClosed() {
		return streamEndShutdown
	}
	if st.r.Context().Err() != nil {
		return streamEndClientGone
	}
	return ""
}

// deliver writes what the open subscriptions have not yet received, one
// frame at a time, and returns "" or the reason the stream ends.
func (st *eventStream) deliver() string {
	wrote := false
	for i := range st.subs {
		sub := &st.subs[i]
		if sub.state != runOpen {
			continue
		}
		if reason := st.interrupted(); reason != "" {
			return reason
		}
		page, running, err := st.readRun(sub)
		if err != nil {
			st.s.logger.Warn("run events read failed",
				slog.String("dispatch_id", sub.dispatchID),
				slog.Any("error", err),
			)
			sub.state = runReadFailed
			continue
		}

		yielded, reason := st.writeRun(sub, page, running)
		if reason != "" {
			return reason
		}
		wrote = wrote || yielded
		if !yielded {
			sub.cursor = min(sub.cursor, page.LastSeq)
		}
	}
	if wrote && st.out.flush() != nil {
		return streamEndWriteFailed
	}
	return ""
}

// readRun reads a running run from memory and any other run from the store,
// always through [orchestrator.PageAfter] so that eviction between reads
// surfaces as an expired page.
func (st *eventStream) readRun(sub *runSubscription) (page orchestrator.RunEventPage, running bool, err error) {
	if st.s.runEventsAfterFn != nil {
		if page, running = st.s.runEventsAfterFn(sub.dispatchID, sub.cursor); running {
			return page, true, nil
		}
	}
	var stored []orchestrator.RunEvent
	if st.s.runHistory != nil {
		if stored, err = st.s.runHistory.RunEvents(st.r.Context(), sub.dispatchID, 0); err != nil {
			return orchestrator.RunEventPage{}, false, err
		}
	}
	return orchestrator.PageAfter(stored, sub.cursor), false, nil
}

// writeRun puts the frames one read yields and reports whether it put any.
func (st *eventStream) writeRun(sub *runSubscription, page orchestrator.RunEventPage, running bool) (bool, string) {
	yielded := false
	emit := func(eventName string, data any) string {
		yielded = true
		return st.emit(eventName, data)
	}

	if page.Expired {
		sub.cursor = page.FirstSeq - 1
		if reason := emit("reset", runResetFrame{DispatchID: sub.dispatchID, Reason: resetCursorExpired, FirstSeq: &page.FirstSeq}); reason != "" {
			return yielded, reason
		}
	}
	for _, served := range servedEvents(page.Events) {
		sub.cursor = served.event.Seq
		if reason := emit("agent_event", agentEventFrame{dispatchID: sub.dispatchID, event: served}); reason != "" {
			return yielded, reason
		}
	}
	if running {
		return yielded, ""
	}
	if page.LastSeq == 0 {
		sub.state = runDropped
		return yielded, emit("reset", runResetFrame{DispatchID: sub.dispatchID, Reason: resetNotRetained})
	}
	sub.state, sub.cursor = runEnded, page.LastSeq
	return yielded, emit("run_ended", runEndedFrame{DispatchID: sub.dispatchID, LastSeq: page.LastSeq})
}

// emit puts one frame under the frame id as the subscriptions stand, then
// writes a change signal that fell due meanwhile.
func (st *eventStream) emit(eventName string, data any) string {
	if reason := st.interrupted(); reason != "" {
		return reason
	}
	frame, err := runFrame(frameID(st.revision, st.subs), eventName, data)
	if err != nil || st.out.put(frame) != nil {
		return streamEndWriteFailed
	}
	if st.changeDue() && st.writeChange() != nil {
		return streamEndWriteFailed
	}
	return ""
}
