package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/orchestrator"
	"github.com/sortie-ai/sortie/internal/sentence"
)

var copiedKeys = []string{
	"issue_id", "issue_identifier", "display_identifier", "issue_title", "issue_url", "rule_name",
	"stage_position", "agent_kind", "configured_model", "dispatched_at", "status_text", "status_text_full",
}

func rowOf[T any](t *testing.T, tag string) T {
	t.Helper()
	const template = `{"issue_id": "id-1", "issue_identifier": "MT-1", "display_identifier": "%[1]s-display",
		"issue_title": "%[1]s title", "issue_url": "https://tracker.example.com/%[1]s", "rule_name": "%[1]s-rule",
		"agent_kind": "%[1]s-agent", "configured_model": "%[1]s-model", "reaction_kind": "ci", "kind": "%[1]s",
		"stage_position": {"chain": ["%[1]s"], "current": "%[1]s", "hop_count": 1, "hop_ceiling": 3},
		"dispatched_at": "2026-02-24T19:31:07Z", "stage_lineage": {"DispatchedAt": "2026-02-24T19:31:07Z"}}`
	var row T
	if err := json.Unmarshal(fmt.Appendf(nil, template, tag), &row); err != nil {
		t.Fatalf("decoding the %s row: %v", tag, err)
	}
	return row
}

func snapshotOf(t *testing.T, tags ...string) orchestrator.RuntimeSnapshotResult {
	t.Helper()
	var snap orchestrator.RuntimeSnapshotResult
	for _, tag := range tags {
		switch tag {
		case "running":
			snap.Running = append(snap.Running, rowOf[orchestrator.SnapshotRunningEntry](t, tag))
		case "retrying":
			snap.Retrying = append(snap.Retrying, rowOf[orchestrator.SnapshotRetryEntry](t, tag))
		case "budget_exhausted":
			snap.BudgetExhausted = append(snap.BudgetExhausted, rowOf[orchestrator.SnapshotBudgetEntry](t, tag))
		case "parked":
			snap.Parked = append(snap.Parked, rowOf[orchestrator.SnapshotParkedEntry](t, tag))
		case "between_stages":
			snap.BetweenStages = append(snap.BetweenStages, rowOf[orchestrator.SnapshotStageHop](t, tag))
		default:
			row := rowOf[orchestrator.SnapshotPendingReaction](t, tag)
			if tag == "ci" {
				row.EscalatedAt = new(row.DispatchedAt.Add(time.Hour))
			}
			snap.PendingReactions = append(snap.PendingReactions, row)
		}
	}
	return snap
}

func decidingRow(t *testing.T, resp stateResponse, status, reactionKind string) map[string]any {
	t.Helper()
	for _, row := range jsonObject(t, resp)[status].([]any) {
		fields := row.(map[string]any)
		if status != "pending_reactions" || fields["kind"] == reactionKind {
			return fields
		}
	}
	t.Fatalf("state holds no %s row of kind %q", status, reactionKind)
	return nil
}

func listedIssueCount(t *testing.T, resp stateResponse) int {
	t.Helper()
	body, ids := jsonObject(t, resp), make(map[any]struct{})
	for _, list := range []string{"running", "retrying", "budget_exhausted", "parked", "pending_reactions", "between_stages"} {
		for _, row := range body[list].([]any) {
			ids[row.(map[string]any)["issue_id"]] = struct{}{}
		}
	}
	return len(ids)
}

func TestActiveIssueServesOneEntryWithTheDecidingRowsFacts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		snap         orchestrator.RuntimeSnapshotResult
		status       string
		reactionKind string
	}{
		{name: "retrying beats parked", snap: snapshotOf(t, "parked", "retrying"), status: "retrying"},
		{name: "retrying beats a pending reaction", snap: snapshotOf(t, "review", "retrying"), status: "retrying"},
		{name: "budget exhausted beats between stages", snap: snapshotOf(t, "between_stages", "budget_exhausted"), status: "budget_exhausted"},
		{name: "escalated pending row decides two pending rows", snap: snapshotOf(t, "review", "ci"), status: "pending_reactions", reactionKind: "ci"},
		{name: "running continuation alone", snap: snapshotOf(t, "running"), status: "running"},
		{name: "retrying alone", snap: snapshotOf(t, "retrying"), status: "retrying"},
		{name: "budget exhausted alone", snap: snapshotOf(t, "budget_exhausted"), status: "budget_exhausted"},
		{name: "parked alone", snap: snapshotOf(t, "parked"), status: "parked"},
		{name: "pending reaction alone", snap: snapshotOf(t, "review"), status: "pending_reactions", reactionKind: "review"},
		{name: "between stages alone", snap: snapshotOf(t, "between_stages"), status: "between_stages"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := toStateResponse(tt.snap, nil, sentence.Context{}, nil)

			if want := listedIssueCount(t, got); len(got.Active) != want {
				t.Fatalf("len(Active) = %d, want %d distinct issues", len(got.Active), want)
			}
			entry, row := jsonObject(t, got.Active[0]), decidingRow(t, got, tt.status, tt.reactionKind)
			for _, key := range copiedKeys {
				if !reflect.DeepEqual(entry[key], row[key]) {
					t.Errorf("Active[0][%q] = %v, want the deciding row's %v", key, entry[key], row[key])
				}
			}
			if gotKind, _ := entry["reaction_kind"].(string); entry["status"] != tt.status || gotKind != tt.reactionKind {
				t.Errorf("Active[0] status, reaction_kind = %v, %q, want %q, %q", entry["status"], gotKind, tt.status, tt.reactionKind)
			}
		})
	}
}

func TestActiveIssueKeepsItsPlaceWhenItMovesFromRunningToRetrying(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 2, 24, 19, 0, 0, 0, time.UTC)
	ids := []string{"id-a", "id-b", "id-c"}
	identifiers := map[string]string{"id-a": "MT-3", "id-b": "MT-1", "id-c": "MT-2"}
	state := orchestrator.NewState(60000, 10, 0, nil, orchestrator.AgentTotals{})
	for i, id := range ids {
		state.Running[id] = &orchestrator.RunningEntry{
			Identifier:   identifiers[id],
			Issue:        domain.Issue{ID: id},
			StageLineage: orchestrator.StageLineage{DispatchedAt: base.Add(time.Duration(i) * time.Minute)},
		}
	}
	servedOrder := func() []string {
		resp := toStateResponse(orchestrator.RuntimeSnapshot(state, base), nil, sentence.Context{}, nil)
		var order []string
		for _, entry := range resp.Active {
			order = append(order, entry.IssueID)
		}
		return order
	}

	before := servedOrder()
	moved := state.Running["id-b"]
	delete(state.Running, "id-b")
	state.RetryAttempts["id-b"] = &orchestrator.RetryEntry{IssueID: "id-b", Identifier: moved.Identifier, StageLineage: moved.StageLineage}
	after := servedOrder()

	if !slices.Equal(before, ids) || !slices.Equal(after, ids) {
		t.Errorf("served issue order before, after the move = %v, %v, want %v both times", before, after, ids)
	}
}
