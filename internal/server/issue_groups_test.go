package server

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/orchestrator"
)

type groupsHistory struct {
	keys     []IssueGroupKey
	runs     []RunHistoryEntry
	outcomes map[string]IssueOutcome
}

func (h groupsHistory) QueryIssueGroups(_ context.Context, q IssueGroupQuery) ([]IssueGroupKey, error) {
	var keys []IssueGroupKey
	for _, key := range h.keys {
		if q.BeforeID == 0 || key.NewestRunID < q.BeforeID {
			keys = append(keys, key)
		}
	}
	return keys[:min(len(keys), q.Limit)], nil
}

func (h groupsHistory) QueryRuns(_ context.Context, q RunHistoryQuery) ([]RunHistoryEntry, error) {
	var rows []RunHistoryEntry
	for _, run := range h.runs {
		if (q.IssueID == "" || run.IssueID == q.IssueID) && (q.Identifier == "" || run.Identifier == q.Identifier) && (q.BeforeID == 0 || run.ID < q.BeforeID) {
			rows = append(rows, run)
		}
	}
	return rows[:min(len(rows), q.Limit)], nil
}

func (h groupsHistory) IssueTotals(context.Context, string) (IssueRunTotals, error) {
	return IssueRunTotals{}, nil
}

func (h groupsHistory) RunEvents(context.Context, string, int) ([]orchestrator.RunEvent, error) {
	return nil, nil
}

func (h groupsHistory) PassActivities(context.Context, []PassRef) (map[PassKey]PassActivity, error) {
	return nil, nil
}

func (h groupsHistory) IssueOutcomes(context.Context, []string) (map[string]IssueOutcome, error) {
	return h.outcomes, nil
}

func TestIssueGroupsRoute(t *testing.T) {
	t.Parallel()

	snap := orchestrator.RuntimeSnapshotResult{Running: []orchestrator.SnapshotRunningEntry{
		{IssueID: "id-live", Identifier: "MT-1", State: "In Progress", StartedAt: time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC)},
	}}
	runs := []RunHistoryEntry{
		{ID: 30, IssueID: "id-many", Identifier: "MT-3", Status: "failed"},
		{ID: 20, IssueID: "id-live", Identifier: "MT-1", Status: "failed"},
		{ID: 15, IssueID: "id-done", Identifier: "MT-2", Status: "succeeded"},
	}
	for id := int64(14); id >= 8; id-- {
		runs = append(runs, RunHistoryEntry{ID: id, IssueID: "id-many", Identifier: "MT-3", Status: "succeeded"})
	}
	history := groupsHistory{
		keys:     []IssueGroupKey{{IssueID: "id-live", NewestRunID: 20}, {IssueID: "id-done", NewestRunID: 15}, {IssueID: "id-many", NewestRunID: 14}},
		runs:     runs,
		outcomes: map[string]IssueOutcome{"id-done": {Kind: "merged", PRNumber: 88}},
	}
	ts := historyServer(t, snap, history)

	t.Run("groups", func(t *testing.T) {
		t.Parallel()

		status, body := getJSON[issueGroupsResponse](t, ts, "/api/v1/runs/issues?limit=3")
		_, live := getJSON[issueDetailResponse](t, ts, "/api/v1/MT-1")

		if status != http.StatusOK || len(body.Groups) != 3 {
			t.Fatalf("GET /api/v1/runs/issues?limit=3 = status %d with %d groups, want 200 with 3", status, len(body.Groups))
		}
		running, finished, many := body.Groups[0], body.Groups[1], body.Groups[2]
		if running.Status != "running" || running.StatusText == "" || running.StatusText != live.StatusText || running.StatusTextFull != live.StatusTextFull {
			t.Errorf("live group status, pair = %q, %q, %q, want %q and the per-issue pair %q, %q",
				running.Status, running.StatusText, running.StatusTextFull, "running", live.StatusText, live.StatusTextFull)
		}
		if finished.Status != "history" || finished.StatusText != "Merged #88" {
			t.Errorf("finished group status, status_text = %q, %q, want %q, %q", finished.Status, finished.StatusText, "history", "Merged #88")
		}
		if finished.IssueOutcome == nil || finished.IssueOutcome.Kind != "merged" {
			t.Errorf("finished group issue_outcome = %+v, want kind %q", finished.IssueOutcome, "merged")
		}
		var gotRuns []int64
		for _, run := range many.Runs {
			gotRuns = append(gotRuns, run.RunID)
		}
		if want := []int64{14, 13, 12, 11, 10}; !slices.Equal(gotRuns, want) {
			t.Errorf("group %q runs = %v, want %v (the newest 5, none newer than its newest matching run)", many.IssueID, gotRuns, want)
		}
		if many.NextCursor == nil || *many.NextCursor != "10" {
			t.Errorf("group %q next_cursor = %v, want %q (the fifth run)", many.IssueID, many.NextCursor, "10")
		}
	})

	pages := []struct {
		limit      int
		wantGroups int
		wantNext   string
	}{
		{limit: 3, wantGroups: 3},
		{limit: 2, wantGroups: 2, wantNext: "15"},
		{limit: 1, wantGroups: 1, wantNext: "20"},
	}
	for _, tt := range pages {
		t.Run("page of "+strconv.Itoa(tt.limit), func(t *testing.T) {
			t.Parallel()

			path := "/api/v1/runs/issues?limit=" + strconv.Itoa(tt.limit)

			_, body := getJSON[issueGroupsResponse](t, ts, path)

			gotNext := ""
			if body.NextCursor != nil {
				gotNext = *body.NextCursor
			}
			if len(body.Groups) != tt.wantGroups || gotNext != tt.wantNext {
				t.Errorf("GET %s = %d groups, next_cursor %q, want %d, %q", path, len(body.Groups), gotNext, tt.wantGroups, tt.wantNext)
			}
		})
	}
}
