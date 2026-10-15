package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/sortie-ai/sortie/internal/runstats"
)

const statsBoundRule = "must be an RFC 3339 timestamp, a date such as 2026-07-01, or a positive duration such as 24h"

var statsParams = []string{"since", "until"}

// StatsFunc computes the run statistics report over the runs completed at or
// after since and before until; a nil bound is open. generatedAt is the
// report time, in UTC. It must be safe for concurrent use.
type StatsFunc func(ctx context.Context, since, until *time.Time, generatedAt time.Time) (runstats.Report, error)

// parseStatsQuery validates every parameter of GET /api/v1/stats before any
// read, so a malformed bound never serves an unbounded report as a bounded
// one. An empty value reads as absent, and a relative bound counts from now.
func parseStatsQuery(values url.Values, now time.Time) (*time.Time, *time.Time, error) {
	if err := checkParamNames(values, statsParams); err != nil {
		return nil, nil, err
	}
	var since, until *time.Time
	for _, bound := range []struct {
		name string
		dest **time.Time
	}{{"since", &since}, {"until", &until}} {
		at, err := runstats.ParseRangeBound(values.Get(bound.name), now)
		if err != nil {
			return nil, nil, &paramError{bound.name, statsBoundRule}
		}
		*bound.dest = at
	}
	if since != nil && until != nil && !since.Before(*until) {
		return nil, nil, &paramError{"since", "must be before until"}
	}
	return since, until, nil
}

func (s *Server) routeStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.methodNotAllowed(w, r, http.MethodGet)
		return
	}
	s.handleStats(w, r)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	if s.stats == nil {
		writeHistoryUnavailable(w, s.logger)
		return
	}

	now := start.UTC()
	since, until, err := parseStatsQuery(r.URL.Query(), now)
	if err != nil {
		s.logger.Debug("stats request rejected",
			slog.String("path", r.URL.Path),
			slog.Any("error", err),
		)
		writeErrorJSON(w, s.logger, http.StatusBadRequest, "invalid_parameter", err.Error())
		return
	}

	report, err := s.stats(r.Context(), since, until, now)
	if r.Context().Err() != nil {
		s.logger.Debug("stats request canceled",
			slog.String("path", r.URL.Path),
			slog.Any("error", err),
		)
		return
	}
	if err != nil {
		s.historyReadFailed(w, r, err)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, report)

	s.logger.Debug("request served",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", http.StatusOK),
		slog.Duration("duration", time.Since(start)),
	)
}
