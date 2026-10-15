package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/orchestrator"
)

func TestStateHealth_VersionStartAndHealthWire(t *testing.T) {
	t.Parallel()

	zone := time.FixedZone("UTC+5:30", 5*3600+30*60)
	startedAt := time.Date(2026, 3, 1, 14, 0, 0, 0, zone)
	failingSince := time.Date(2026, 3, 1, 15, 0, 0, 0, zone)
	checkedAt := time.Date(2026, 3, 1, 15, 5, 0, 0, zone)

	srv := New(Params{
		SnapshotFn: fixedSnapshot(orchestrator.RuntimeSnapshotResult{GeneratedAt: time.Now().UTC()}),
		RefreshFn:  acceptingRefresh(),
		Logger:     slog.New(slog.DiscardHandler),
		Version:    "1.2.3",
		StartedAt:  startedAt,
		HealthFn: func() orchestrator.SnapshotHealth {
			return orchestrator.SnapshotHealth{
				Workflow: orchestrator.SnapshotWorkflowHealth{
					File: "WORKFLOW.md",
					SnapshotHealthCheck: orchestrator.SnapshotHealthCheck{
						CheckedAt:           checkedAt,
						ConsecutiveFailures: 2,
						FailingSince:        failingSince,
						Error:               "parse front matter",
					},
				},
			}
		},
	})
	ts := httptest.NewServer(srv.Mux())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/v1/state")
	if err != nil {
		t.Fatalf("GET /api/v1/state: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body := decodeJSON[map[string]any](t, resp)
	health, ok := body["health"].(map[string]any)
	if !ok {
		t.Fatalf("health = %#v, want an object", body["health"])
	}
	workflow, _ := health["workflow"].(map[string]any)
	database, _ := health["database"].(map[string]any)

	if got := body["version"]; got != "1.2.3" {
		t.Errorf("version = %#v, want %q", got, "1.2.3")
	}
	if got := body["started_at"]; got != "2026-03-01T08:30:00Z" {
		t.Errorf("started_at = %#v, want %q", got, "2026-03-01T08:30:00Z")
	}
	if got := workflow["status"]; got != "fail" {
		t.Errorf("health.workflow.status = %#v, want %q", got, "fail")
	}
	if got := workflow["error"]; got != "parse front matter" {
		t.Errorf("health.workflow.error = %#v, want %q", got, "parse front matter")
	}
	if got := workflow["failing_since"]; got != "2026-03-01T09:30:00Z" {
		t.Errorf("health.workflow.failing_since = %#v, want %q", got, "2026-03-01T09:30:00Z")
	}
	if got := database["status"]; got != "pass" {
		t.Errorf("health.database.status = %#v, want %q", got, "pass")
	}
	for _, key := range []string{"checked_at", "last_success_at", "failing_since"} {
		if got, present := database[key]; !present || got != nil {
			t.Errorf("health.database.%s = %#v (present=%t), want present and null", key, got, present)
		}
	}
	if _, present := database["error"]; present {
		t.Errorf("health.database has an error key = %#v, want none", database["error"])
	}
	if got, present := health["scm"]; !present || got != nil {
		t.Errorf("health.scm = %#v (present=%t), want present and null", got, present)
	}
}
