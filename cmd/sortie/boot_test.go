package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/workflow"
)

func notificationsWorkflow(url string) []byte {
	return []byte(`---
tracker:
  kind: file
  active_states:
    - To Do
  terminal_states:
    - Done
agent:
  kind: mock
notifications:
  - kind: webhook
    url: "` + url + `"
    events: [budget.held]
---
Do {{ .issue.title }}.
`)
}

func TestWorkflowValidate_RejectedReloadKeepsPreviousConfiguration(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	if err := os.WriteFile(path, notificationsWorkflow("https://example.com/hook"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	mgr, err := workflow.NewManager(path, slog.New(slog.DiscardHandler),
		workflow.WithValidateFunc(workflowValidate),
		workflow.WithAgentKindProbe(registry.Agents.Has),
		workflow.WithRetiredAgents(registry.RetiredAgentOf),
		workflow.WithAdvisoryFunc(workflowAdvisories))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	before := mgr.Config().Notifications

	if err := os.WriteFile(path, notificationsWorkflow(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	reloadErr := mgr.Reload()

	if reloadErr == nil {
		t.Fatal("Reload() error = nil, want the unconstructible destination rejected")
	}
	if mgr.LastLoadError() == nil {
		t.Error("LastLoadError() = nil, want the rejected reload recorded")
	}
	if after := mgr.Config().Notifications; !reflect.DeepEqual(after, before) {
		t.Errorf("Notifications after a rejected reload = %+v, want the previous %+v", after, before)
	}
}

func TestWorkflowValidate_StartupRejectsUnconstructibleDestination(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	if err := os.WriteFile(path, notificationsWorkflow(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := workflow.NewManager(path, slog.New(slog.DiscardHandler),
		workflow.WithValidateFunc(workflowValidate),
		workflow.WithAgentKindProbe(registry.Agents.Has))

	if err == nil {
		t.Fatal("NewManager() error = nil, want the unconstructible destination to fail the load")
	}
}

func TestRunOnceWithDryRun(t *testing.T) {
	t.Parallel()

	var stderr lockedBuf
	args := []string{"--once", "--dry-run", filepath.Join(t.TempDir(), "missing", "WORKFLOW.md")}

	code := run(t.Context(), args, io.Discard, &stderr)

	if want := "--once and --dry-run cannot be used together"; code != 1 || !strings.Contains(stderr.String(), want) {
		t.Errorf("run(%q) = %d, stderr %q, want 1 and stderr containing %q", args, code, stderr.String(), want)
	}
}
