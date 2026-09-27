package pi_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"

	_ "github.com/sortie-ai/sortie/internal/agent/pi"
	"github.com/sortie-ai/sortie/internal/registry"
)

func skipIfNotEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("SORTIE_PI_TEST") != "1" {
		t.Skip("set SORTIE_PI_TEST=1 to run pi integration tests")
	}
}

func integrationCommand() string {
	if command := os.Getenv("SORTIE_PI_COMMAND"); command != "" {
		return command
	}
	return "pi"
}

func integrationConfig() map[string]any {
	config := map[string]any{
		"project_trust": "ignore",
	}
	if model := os.Getenv("SORTIE_PI_MODEL"); model != "" {
		config["model"] = model
	}
	return config
}

func newAdapter(t *testing.T, config map[string]any) domain.AgentAdapter {
	t.Helper()
	factory, err := registry.Agents.Get("pi")
	if err != nil {
		t.Fatalf("registry.Agents.Get(pi): %v", err)
	}
	adapter, err := factory(config)
	if err != nil {
		t.Fatalf("factory(pi): %v", err)
	}
	return adapter
}

func startSession(t *testing.T, adapter domain.AgentAdapter, workspace, resumeID string) domain.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := adapter.StartSession(ctx, domain.StartSessionParams{
		WorkspacePath:   workspace,
		ResumeSessionID: resumeID,
		AgentConfig: domain.AgentConfig{
			Command:       integrationCommand(),
			ReadTimeoutMS: 3 * 60 * 1000,
			StopGraceMS:   10 * 1000,
		},
	})
	if err != nil {
		t.Fatalf("StartSession(): %v", err)
	}
	return session
}

func runTurn(t *testing.T, adapter domain.AgentAdapter, session domain.Session, prompt string) ([]domain.AgentEvent, domain.TurnResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var events []domain.AgentEvent
	result, err := adapter.RunTurn(ctx, session, domain.RunTurnParams{
		Prompt: prompt,
		OnEvent: func(event domain.AgentEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Logf("RunTurn() error: %v", err)
	}
	return events, result
}

func TestIntegration_HappyPathFreshTurn(t *testing.T) {
	skipIfNotEnabled(t)

	adapter := newAdapter(t, integrationConfig())
	session := startSession(t, adapter, t.TempDir(), "")
	t.Cleanup(func() { _ = adapter.StopSession(context.Background(), session) })

	events, result := runTurn(t, adapter, session, "Reply with exactly: hello")
	if result.ExitReason != domain.EventTurnCompleted {
		t.Fatalf("ExitReason = %q, want %q; events=%+v", result.ExitReason, domain.EventTurnCompleted, events)
	}
	if result.SessionID == "" {
		t.Error("TurnResult.SessionID is empty")
	}
	if !hasEvent(events, domain.EventSessionStarted) {
		t.Error("no session_started event emitted")
	}
	if !result.UsageMeasured || result.Usage.TotalTokens == 0 {
		t.Errorf("usage = %+v, measured = %t; want a non-zero measured figure", result.Usage, result.UsageMeasured)
	}
}

func TestIntegration_SessionResume(t *testing.T) {
	skipIfNotEnabled(t)

	workspace := t.TempDir()
	adapter := newAdapter(t, integrationConfig())
	first := startSession(t, adapter, workspace, "")
	t.Cleanup(func() { _ = adapter.StopSession(context.Background(), first) })

	_, firstResult := runTurn(t, adapter, first, "Reply with exactly: first")
	if firstResult.ExitReason != domain.EventTurnCompleted {
		t.Fatalf("first turn ExitReason = %q, want %q", firstResult.ExitReason, domain.EventTurnCompleted)
	}
	if firstResult.SessionID == "" {
		t.Fatal("first turn returned no session id")
	}

	resumedAdapter := newAdapter(t, integrationConfig())
	resumed := startSession(t, resumedAdapter, workspace, firstResult.SessionID)
	t.Cleanup(func() { _ = resumedAdapter.StopSession(context.Background(), resumed) })

	_, resumedResult := runTurn(t, resumedAdapter, resumed, "Reply with exactly: second")
	if resumedResult.ExitReason != domain.EventTurnCompleted {
		t.Errorf("resumed turn ExitReason = %q, want %q", resumedResult.ExitReason, domain.EventTurnCompleted)
	}
}

func TestIntegration_InvalidModelFailure(t *testing.T) {
	skipIfNotEnabled(t)

	config := integrationConfig()
	config["model"] = "sortie-invalid/nonexistent-model"
	adapter := newAdapter(t, config)
	session := startSession(t, adapter, t.TempDir(), "")
	t.Cleanup(func() { _ = adapter.StopSession(context.Background(), session) })

	events, result := runTurn(t, adapter, session, "Reply with exactly: hello")
	if result.ExitReason != domain.EventTurnFailed {
		t.Fatalf("ExitReason = %q, want %q; events=%+v", result.ExitReason, domain.EventTurnFailed, events)
	}
}

func TestIntegration_ToolAndUsage(t *testing.T) {
	skipIfNotEnabled(t)

	adapter := newAdapter(t, integrationConfig())
	session := startSession(t, adapter, t.TempDir(), "")
	t.Cleanup(func() { _ = adapter.StopSession(context.Background(), session) })

	events, result := runTurn(t, adapter, session,
		"Use the bash tool to run `printf pi-tool-ok`, then reply with exactly: pi-tool-ok. Do not skip the tool call.")
	if result.ExitReason != domain.EventTurnCompleted {
		t.Fatalf("ExitReason = %q, want %q; events=%+v", result.ExitReason, domain.EventTurnCompleted, events)
	}

	var sawTool bool
	for _, event := range events {
		if event.Type == domain.EventToolResult {
			sawTool = true
			break
		}
	}
	if !sawTool {
		t.Skip("model completed without a tool call; skipping a non-deterministic tool assertion")
	}
	if !result.UsageMeasured || result.Usage.TotalTokens == 0 {
		t.Errorf("usage = %+v, measured = %t; want a non-zero measured figure", result.Usage, result.UsageMeasured)
	}
}

func TestIntegration_TurnCancellation(t *testing.T) {
	skipIfNotEnabled(t)

	adapter := newAdapter(t, integrationConfig())
	session := startSession(t, adapter, t.TempDir(), "")
	t.Cleanup(func() { _ = adapter.StopSession(context.Background(), session) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	turnCtx, turnCancel := context.WithCancel(ctx)
	resultCh := make(chan domain.TurnResult, 1)
	go func() {
		result, _ := adapter.RunTurn(turnCtx, session, domain.RunTurnParams{
			Prompt:  "Count slowly from 1 to 1000, putting each number on its own line",
			OnEvent: func(domain.AgentEvent) {},
		})
		resultCh <- result
	}()

	time.Sleep(500 * time.Millisecond)
	turnCancel()

	select {
	case result := <-resultCh:
		if result.ExitReason != domain.EventTurnCancelled {
			t.Errorf("ExitReason = %q, want %q", result.ExitReason, domain.EventTurnCancelled)
		}
	case <-ctx.Done():
		t.Fatal("RunTurn did not return after context cancellation")
	}
}

func hasEvent(events []domain.AgentEvent, eventType domain.AgentEventType) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
