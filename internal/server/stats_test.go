package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/orchestrator"
	"github.com/sortie-ai/sortie/internal/runstats"
)

type statsCall struct {
	since, until *time.Time
	generatedAt  time.Time
}

func TestStatsQueryBoundary(t *testing.T) {
	t.Parallel()

	july1 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		query     string
		wantParam string
		wantSince *time.Time
		wantAgo   time.Duration
		failWith  error
	}{
		{name: "unknown parameter", query: "format=json", wantParam: "format"},
		{name: "repeated parameter", query: "since=24h&since=48h", wantParam: "since"},
		{name: "day-suffixed duration", query: "since=7d", wantParam: "since"},
		{name: "zero duration", query: "until=0s", wantParam: "until"},
		{name: "timestamp without offset", query: "since=2026-07-01T00:00:00", wantParam: "since"},
		{name: "since after until as dates", query: "since=2026-07-02&until=2026-07-01", wantParam: "since"},
		{name: "since after until as durations", query: "since=24h&until=48h", wantParam: "since"},
		{name: "empty values read as absent", query: "since=&until="},
		{name: "date reads as midnight UTC", query: "since=2026-07-01", wantSince: &july1},
		{name: "duration counts back from the report time", query: "since=168h", wantAgo: 168 * time.Hour},
		{name: "stats failure answers 503", failWith: errors.New("db closed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls := make(chan statsCall, 2)
			srv := New(Params{
				SnapshotFn: fixedSnapshot(orchestrator.RuntimeSnapshotResult{}),
				RefreshFn:  acceptingRefresh(),
				Logger:     slog.New(slog.DiscardHandler),
				Stats: func(_ context.Context, since, until *time.Time, generatedAt time.Time) (runstats.Report, error) {
					calls <- statsCall{since, until, generatedAt}
					return runstats.Report{}, tt.failWith
				},
			})
			ts := httptest.NewServer(srv.Mux())
			t.Cleanup(ts.Close)

			status, body := getJSON[errorResponse](t, ts, "/api/v1/stats?"+tt.query)

			wantStatus, wantCode, wantCalls := http.StatusOK, "", 1
			switch {
			case tt.wantParam != "":
				wantStatus, wantCode, wantCalls = http.StatusBadRequest, "invalid_parameter", 0
			case tt.failWith != nil:
				wantStatus, wantCode = http.StatusServiceUnavailable, "history_unavailable"
			}
			if status != wantStatus || body.Error.Code != wantCode {
				t.Fatalf("GET /api/v1/stats?%s = %d %q, want %d %q", tt.query, status, body.Error.Code, wantStatus, wantCode)
			}
			if got := len(calls); got != wantCalls {
				t.Fatalf("GET /api/v1/stats?%s called Stats %d times, want %d", tt.query, got, wantCalls)
			}
			if tt.wantParam != "" && !strings.Contains(body.Error.Message, `"`+tt.wantParam+`"`) {
				t.Errorf("GET /api/v1/stats?%s error message = %q, want it to name parameter %q", tt.query, body.Error.Message, tt.wantParam)
			}
			if wantStatus != http.StatusOK {
				return
			}

			call := <-calls
			if tt.wantAgo != 0 {
				ago := call.generatedAt.Add(-tt.wantAgo)
				tt.wantSince = &ago
			}
			if (call.since == nil) != (tt.wantSince == nil) || (tt.wantSince != nil && !call.since.Equal(*tt.wantSince)) {
				t.Errorf("GET /api/v1/stats?%s since = %v, want %v", tt.query, call.since, tt.wantSince)
			}
			if call.until != nil {
				t.Errorf("GET /api/v1/stats?%s until = %v, want nil", tt.query, call.until)
			}
		})
	}
}
