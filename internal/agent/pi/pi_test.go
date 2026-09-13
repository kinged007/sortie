package pi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/domain"
)

// writePiScript writes an executable shell script named fake-pi in dir
// with the given body and returns its path.
func writePiScript(t *testing.T, dir, body string) string {
	t.Helper()
	return agenttest.WriteScript(t, dir, "fake-pi", body)
}

// mustStartSession starts a session with the given command or fatals.
func mustStartSession(t *testing.T, a domain.AgentAdapter, workDir, cmd string) domain.Session {
	t.Helper()
	session, err := a.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath: workDir,
		AgentConfig:   domain.AgentConfig{Command: cmd},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return session
}

// collectEvents runs a turn and collects all emitted events.
func collectEvents(t *testing.T, a domain.AgentAdapter, session domain.Session, prompt string) ([]domain.AgentEvent, domain.TurnResult, error) {
	t.Helper()
	var events []domain.AgentEvent
	result, err := a.RunTurn(context.Background(), session, domain.RunTurnParams{
		Prompt: prompt,
		OnEvent: func(e domain.AgentEvent) {
			events = append(events, e)
		},
	})
	return events, result, err
}

// writeRunFixtureScript writes fixtureName into dir and returns a fake-pi
// script that cats it for any invocation.
func writeRunFixtureScript(t *testing.T, dir, fixtureName string) string {
	t.Helper()
	runPath := filepath.Join(dir, fixtureName)
	data, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", fixtureName, err)
	}
	if err := os.WriteFile(runPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", fixtureName, err)
	}
	return writePiScript(t, dir, "cat '"+runPath+"'")
}

func hasNotification(events []domain.AgentEvent, message string) bool {
	for _, e := range events {
		if e.Type == domain.EventNotification && e.Message == message {
			return true
		}
	}
	return false
}

func TestRunTurn_SimpleText(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, "simple_turn.jsonl"))
	events, result, err := collectEvents(t, a, session, "do work")
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if !hasNotification(events, "Hello, world!") {
		t.Errorf("missing assistant text notification, events = %+v", events)
	}
	if result.Usage.InputTokens != 110 || result.Usage.OutputTokens != 20 {
		t.Errorf("usage = %+v, want input 100 output 20", result.Usage)
	}
	if err := a.StopSession(context.Background(), session); err != nil {
		t.Fatalf("StopSession() error = %v", err)
	}
}

func TestRunTurn_ToolCall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, "tool_success.jsonl"))
	events, _, err := collectEvents(t, a, session, "write it")
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	found := false
	for _, e := range events {
		if e.Type == domain.EventToolResult && e.ToolName == "write" && !e.ToolError {
			found = true
		}
	}
	if !found {
		t.Errorf("missing write tool result, events = %+v", events)
	}
}

func TestRunTurn_ResumeSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, "resume_turn.jsonl"))
	events, _, err := collectEvents(t, a, session, "continue")
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	started := false
	for _, e := range events {
		if e.Type == domain.EventSessionStarted && e.SessionID == "ses_resume1" {
			started = true
		}
	}
	if !started {
		t.Errorf("missing session-started for ses_resume1, events = %+v", events)
	}
}

// TestAssertUsageReporting proves pi's registered usage-reporting
// declaration (turn_end, per_model) against a real event stream: one
// usage figure, carried on the stream's turn_end event, arriving after
// the run's tool result.
func TestAssertUsageReporting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, "tool_success.jsonl"))
	events, result, err := collectEvents(t, a, session, "work")
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	agenttest.AssertUsageReporting(t, "pi", []agenttest.UsageReportingCase{
		{Name: "one figure after the tool result", Events: events, Result: result},
	})
}

func TestRunTurn_NonZeroExitFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	script := writePiScript(t, dir, `echo '{"type":"session","id":"ses_x"}'; exit 3`)
	session := mustStartSession(t, a, dir, script)
	_, _, err = collectEvents(t, a, session, "do work")
	if err == nil {
		t.Fatal("RunTurn() error = nil, want port_exit failure")
	}
}
