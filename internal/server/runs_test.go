package server

import (
	"cmp"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/orchestrator"
	"github.com/sortie-ai/sortie/internal/redact"
)

func pricedRates() TokenRates {
	return TokenRates{"claude-code": {InputPerMtok: new(3.0), OutputPerMtok: new(15.0)}}
}

func costOrNil(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestRunsQueryBoundary(t *testing.T) {
	t.Parallel()

	const groupsPath = "/api/v1/runs/issues"
	tests := []struct {
		name      string
		path      string
		query     string
		wantParam string
	}{
		{name: "unknown parameter", query: "bogus=1", wantParam: "bogus"},
		{name: "repeated parameter", query: "rule=a&rule=b", wantParam: "rule"},
		{name: "limit below range", query: "limit=0", wantParam: "limit"},
		{name: "limit above range", query: "limit=201", wantParam: "limit"},
		{name: "limit not a number", query: "limit=x", wantParam: "limit"},
		{name: "cursor below range", query: "cursor=0", wantParam: "cursor"},
		{name: "cursor not a number", query: "cursor=x", wantParam: "cursor"},
		{name: "malformed since", query: "since=yesterday", wantParam: "since"},
		{name: "malformed until", query: "until=tomorrow", wantParam: "until"},
		{name: "since equals until as instants", query: "since=2026-03-24T09:00:00Z&until=2026-03-24T11:00:00%2B02:00", wantParam: "since"},
		{name: "ended_since equals ended_until", query: "ended_since=2026-03-24T09:00:00Z&ended_until=2026-03-24T09:00:00Z", wantParam: "ended_since"},
		{name: "empty values read as absent", query: "limit=&cursor=&rule=&since=2026-03-24T09:00:00Z&until=2026-03-24T12:00:00%2B02:00"},
		{name: "limit at lower bound", query: "limit=1"},
		{name: "limit at upper bound", query: "limit=200"},
		{name: "cursor at lower bound", query: "cursor=1"},
		{name: "issues: limit below range", path: groupsPath, query: "limit=0", wantParam: "limit"},
		{name: "issues: limit above range", path: groupsPath, query: "limit=51", wantParam: "limit"},
		{name: "issues: cursor below range", path: groupsPath, query: "cursor=0", wantParam: "cursor"},
		{name: "issues: parameter of the flat route", path: groupsPath, query: "ended_since=2026-03-24T09:00:00Z", wantParam: "ended_since"},
		{name: "issues: repeated issue", path: groupsPath, query: "issue=a&issue=b", wantParam: "issue"},
		{name: "issues: empty values read as absent", path: groupsPath, query: "issue=&rule=&status=&stage=&cursor=&limit="},
		{name: "issues: limit at upper bound", path: groupsPath, query: "limit=50"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := cmp.Or(tt.path, "/api/v1/runs") + "?" + tt.query
			var reads atomic.Int32
			ts := historyServer(t, orchestrator.RuntimeSnapshotResult{}, &fakeRunHistory{
				onQuery:  func(RunHistoryQuery) { reads.Add(1) },
				onGroups: func(IssueGroupQuery) { reads.Add(1) },
			})

			status, body := getJSON[errorResponse](t, ts, path)

			wantStatus, wantReads := http.StatusOK, int32(1)
			if tt.wantParam != "" {
				wantStatus, wantReads = http.StatusBadRequest, 0
			}
			if status != wantStatus {
				t.Fatalf("GET %s status = %d, want %d", path, status, wantStatus)
			}
			if got := reads.Load(); got != wantReads {
				t.Errorf("GET %s read run history %d times, want %d", path, got, wantReads)
			}
			if tt.wantParam == "" {
				return
			}
			if body.Error.Code != "invalid_parameter" {
				t.Errorf("GET %s error code = %q, want %q", path, body.Error.Code, "invalid_parameter")
			}
			if !strings.Contains(body.Error.Message, `"`+tt.wantParam+`"`) {
				t.Errorf("GET %s error message = %q, want it to name parameter %q", path, body.Error.Message, tt.wantParam)
			}
		})
	}
}

func TestRunsFailedOutcomeReadAnswers503(t *testing.T) {
	t.Parallel()

	ts := historyServer(t, orchestrator.RuntimeSnapshotResult{}, &fakeRunHistory{
		rows:        []RunHistoryEntry{{ID: 3, IssueID: "id-1", Identifier: "MT-100", Status: "succeeded"}},
		outcomesErr: errors.New("db closed"),
	})

	status, body := getJSON[errorResponse](t, ts, "/api/v1/runs")

	if status != http.StatusServiceUnavailable || body.Error.Code != "history_unavailable" {
		t.Errorf("GET /api/v1/runs = %d %q, want %d %q", status, body.Error.Code, http.StatusServiceUnavailable, "history_unavailable")
	}
}

func TestRunsMaskRegisteredValues(t *testing.T) {
	t.Parallel()

	const secret = "runs-test-credential-7c1d9e"
	redact.Add("test.runs response masking", secret)

	failure := "agent failed with " + secret
	review := `{"enabled":true,"final_verdict":"blocked by ` + secret + `","total_iterations":1}`
	ts := historyServer(t, orchestrator.RuntimeSnapshotResult{}, &fakeRunHistory{rows: []RunHistoryEntry{{
		ID:             7,
		IssueID:        "id-1",
		Identifier:     "MT-1",
		IssueTitle:     "Fix " + secret,
		IssueURL:       "https://tracker.example/" + secret,
		Status:         "failed",
		Error:          &failure,
		ReviewMetadata: &review,
	}}})

	status, got := getJSON[runsResponse](t, ts, "/api/v1/runs")

	if status != http.StatusOK || len(got.Runs) != 1 {
		t.Fatalf("GET /api/v1/runs = status %d with %d runs, want 200 with 1", status, len(got.Runs))
	}
	run := got.Runs[0]
	if run.Error == nil || run.SelfReview == nil {
		t.Fatalf("GET /api/v1/runs run error, self_review = %v, %v, want both present", run.Error, run.SelfReview)
	}
	served := map[string]string{
		"issue_title":               run.IssueTitle,
		"issue_url":                 run.IssueURL,
		"error":                     *run.Error,
		"self_review.final_verdict": run.SelfReview.FinalVerdict,
	}
	for field, value := range served {
		if strings.Contains(value, secret) {
			t.Errorf("GET /api/v1/runs %s = %q, want the registered value masked", field, value)
		}
		if !strings.Contains(value, redact.Marker) {
			t.Errorf("GET /api/v1/runs %s = %q, want it to hold %q", field, value, redact.Marker)
		}
	}
}

func TestRunCostOf(t *testing.T) {
	t.Parallel()

	spend := domain.TokenUsage{InputTokens: 1_000_000, OutputTokens: 100_000, TotalTokens: 1_100_000}

	tests := []struct {
		name       string
		entry      RunHistoryEntry
		wantCost   *float64
		wantStatus string
	}{
		{
			name:       "unmeasured",
			entry:      RunHistoryEntry{AgentKind: "claude-code"},
			wantStatus: costStatusUnmeasured,
		},
		{
			name:       "measured zero without a rate",
			entry:      RunHistoryEntry{TokensMeasured: true},
			wantCost:   new(0.0),
			wantStatus: costStatusEstimated,
		},
		{
			name:       "priced",
			entry:      RunHistoryEntry{AgentKind: "claude-code", TokensMeasured: true, Tokens: spend},
			wantCost:   new(4.5),
			wantStatus: costStatusEstimated,
		},
		{
			name:       "priced with an unaccounted turn",
			entry:      RunHistoryEntry{AgentKind: "claude-code", TokensMeasured: true, Tokens: spend, UnaccountedTurns: 1},
			wantCost:   new(4.5),
			wantStatus: costStatusPartial,
		},
		{
			name:       "measured spend of an unpriced kind",
			entry:      RunHistoryEntry{AgentKind: "other", TokensMeasured: true, Tokens: spend},
			wantStatus: costStatusUnpriced,
		},
		{
			name:       "zero classes beside a total of an unpriced kind",
			entry:      RunHistoryEntry{AgentKind: "other", TokensMeasured: true, Tokens: domain.TokenUsage{TotalTokens: 500}},
			wantStatus: costStatusUnpriced,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotCost, gotStatus := runCostOf(tt.entry, pricedRates())

			if !reflect.DeepEqual(gotCost, tt.wantCost) || gotStatus != tt.wantStatus {
				t.Errorf("runCostOf(%+v) = %v, %q, want %v, %q", tt.entry, costOrNil(gotCost), gotStatus, costOrNil(tt.wantCost), tt.wantStatus)
			}
		})
	}
}

func TestHistoryTotalsOf(t *testing.T) {
	t.Parallel()

	spend := domain.TokenUsage{InputTokens: 1_000_000, OutputTokens: 100_000, TotalTokens: 1_100_000}
	byKind := map[string]domain.TokenUsage{"claude-code": spend}
	partialTokens := tokenInfo{
		InputTokens: new(int64(1_000_000)), OutputTokens: new(int64(100_000)), TotalTokens: new(int64(1_100_000)),
	}

	tests := []struct {
		name string
		in   IssueRunTotals
		want historyTotalsResponse
	}{
		{
			name: "every run measured and priced",
			in: IssueRunTotals{
				Usage:  orchestrator.SnapshotIssueUsage{Sessions: 2, TotalTokens: 1_100_000, ByKind: byKind},
				Tokens: domain.TokenUsage{InputTokens: 1_000_000, OutputTokens: 100_000, CacheReadTokens: 300_000},
			},
			want: historyTotalsResponse{
				RunCount: 2,
				Tokens: tokenInfo{
					InputTokens: new(int64(1_000_000)), OutputTokens: new(int64(100_000)), TotalTokens: new(int64(1_100_000)),
					CacheReadTokens: new(int64(300_000)), CacheWriteTokens: new(int64(0)),
				},
				UncachedTokens:   new(int64(800_000)),
				TokensComplete:   true,
				EstimatedCostUSD: new(4.5),
				CostStatus:       costStatusEstimated,
			},
		},
		{
			name: "unmeasured run beside priced spend",
			in: IssueRunTotals{
				Usage:  orchestrator.SnapshotIssueUsage{Sessions: 3, UnmeasuredSessions: 1, TotalTokens: 1_100_000, ByKind: byKind},
				Tokens: spend,
			},
			want: historyTotalsResponse{
				RunCount:         3,
				UnmeasuredRuns:   1,
				Tokens:           partialTokens,
				UncachedTokens:   new(int64(1_100_000)),
				EstimatedCostUSD: new(4.5),
				CostStatus:       costStatusPartial,
			},
		},
		{
			name: "unaccounted turn beside priced spend",
			in: IssueRunTotals{
				Usage:  orchestrator.SnapshotIssueUsage{Sessions: 2, UnaccountedTurns: 1, TotalTokens: 1_100_000, ByKind: byKind},
				Tokens: spend,
			},
			want: historyTotalsResponse{
				RunCount:         2,
				Tokens:           partialTokens,
				UncachedTokens:   new(int64(1_100_000)),
				EstimatedCostUSD: new(4.5),
				CostStatus:       costStatusPartial,
			},
		},
		{
			name: "unmeasured run and nothing else",
			in:   IssueRunTotals{Usage: orchestrator.SnapshotIssueUsage{Sessions: 1, UnmeasuredSessions: 1}},
			want: historyTotalsResponse{RunCount: 1, UnmeasuredRuns: 1, CostStatus: costStatusUnmeasured},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := historyTotalsOf(tt.in, pricedRates())

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("historyTotalsOf(%+v) = %v, want %v", tt.in, jsonObject(t, got), jsonObject(t, tt.want))
			}
		})
	}
}
