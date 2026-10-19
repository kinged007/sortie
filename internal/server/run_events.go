package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/sortie-ai/sortie/internal/orchestrator"
	"github.com/sortie-ai/sortie/internal/redact"
	"github.com/sortie-ai/sortie/internal/sentence"
)

// recentEventsLimit is the most events recent_events serves.
const recentEventsLimit = 50

// runEventResponse is one served event: its position, its time and type, the
// members its type carries, and its sentence.
type runEventResponse struct {
	event orchestrator.RunEvent
	text  string
}

// MarshalJSON writes seq, at, event, the members of the event's payload, and
// text. The payload encoder alone decides which members an event carries.
func (r runEventResponse) MarshalJSON() ([]byte, error) {
	head, err := json.Marshal(struct {
		Seq   int64     `json:"seq"`
		At    time.Time `json:"at"`
		Event string    `json:"event"`
	}{r.event.Seq, r.event.At, string(r.event.Event)})
	if err != nil {
		return nil, err
	}
	text, err := json.Marshal(r.text)
	if err != nil {
		return nil, err
	}
	payload := r.event.Payload()

	var out bytes.Buffer
	out.Write(head[:len(head)-1])
	out.WriteByte(',')
	out.Write(payload[1 : len(payload)-1])
	out.WriteString(`,"text":`)
	out.Write(text)
	out.WriteByte('}')
	return out.Bytes(), nil
}

// runEventsResponse is the body of GET /api/v1/runs/{dispatch_id}/events.
type runEventsResponse struct {
	DispatchID string             `json:"dispatch_id"`
	FirstSeq   *int64             `json:"first_seq"`
	Events     []runEventResponse `json:"events"`
}

// servedEvents builds the served form of events: each message masked again,
// since a secret may have been registered after the event was retained, and
// each event worded. It never returns nil.
func servedEvents(events []orchestrator.RunEvent) []runEventResponse {
	served := make([]runEventResponse, len(events))
	for i, event := range events {
		event.Message = redact.Mask(event.Message)
		served[i] = runEventResponse{event: event, text: sentence.Event(event)}
	}
	return served
}

func (s *Server) routeRunEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.methodNotAllowed(w, r, http.MethodGet)
		return
	}
	s.handleRunEvents(w, r)
}

func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	dispatchID := r.PathValue("dispatch_id")

	events, running := s.runningEvents(dispatchID)
	if !running {
		if s.runHistory == nil {
			writeHistoryUnavailable(w, s.logger)
			return
		}
		stored, err := s.runHistory.RunEvents(r.Context(), dispatchID, 0)
		if err != nil {
			s.historyReadFailed(w, r, err)
			return
		}
		if len(stored) == 0 {
			writeErrorJSON(w, s.logger, http.StatusNotFound,
				"run_events_not_retained",
				"the events of this run are not retained")
			return
		}
		events = stored
	}

	resp := runEventsResponse{DispatchID: dispatchID, Events: servedEvents(events)}
	if len(events) > 0 {
		resp.FirstSeq = &events[0].Seq
	}
	writeJSON(w, s.logger, http.StatusOK, resp)

	s.logger.Debug("request served",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", http.StatusOK),
		slog.Duration("duration", time.Since(start)),
	)
}

// runningEvents returns the events of a running run, and false when no
// running run carries dispatchID or the server has no source for them.
func (s *Server) runningEvents(dispatchID string) ([]orchestrator.RunEvent, bool) {
	if s.runEventsFn == nil {
		return nil, false
	}
	return s.runEventsFn(dispatchID)
}

// recentEvents selects the events recent_events serves for detail: those of
// the running run, else those of the newest run that has a dispatch ID. It
// returns an error for a failed store read, and never a nil slice.
func (s *Server) recentEvents(ctx context.Context, detail *issueDetailResponse) ([]runEventResponse, error) {
	dispatchID, err := s.recentEventsRun(ctx, detail)
	if err != nil || dispatchID == "" {
		return []runEventResponse{}, err
	}

	if events, running := s.runningEvents(dispatchID); running {
		return servedEvents(events[max(len(events)-recentEventsLimit, 0):]), nil
	}
	if s.runHistory == nil {
		return []runEventResponse{}, nil
	}
	stored, err := s.runHistory.RunEvents(ctx, dispatchID, recentEventsLimit)
	if err != nil {
		return nil, err
	}
	return servedEvents(stored), nil
}

// recentEventsRun returns the dispatch ID of the run recent_events serves, or
// "" when the issue has none. A running run reads no history; otherwise the
// newest run with a dispatch ID decides, found beyond the attached history
// page by one more read when the page holds none.
func (s *Server) recentEventsRun(ctx context.Context, detail *issueDetailResponse) (string, error) {
	if detail.Running != nil {
		return detail.Running.DispatchID, nil
	}
	if detail.History == nil {
		return "", nil
	}
	if i := slices.IndexFunc(detail.History.Runs, hasDispatchID); i >= 0 {
		return *detail.History.Runs[i].DispatchID, nil
	}
	if detail.History.NextCursor == nil {
		return "", nil
	}
	runs, err := s.runHistory.QueryRuns(ctx, RunHistoryQuery{IssueID: detail.IssueID, HasDispatchID: true, Limit: 1})
	if err != nil || len(runs) == 0 || runs[0].DispatchID == nil {
		return "", err
	}
	return *runs[0].DispatchID, nil
}

func hasDispatchID(run runResponse) bool {
	return run.DispatchID != nil && *run.DispatchID != ""
}
