package pi

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
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

// writeRunFixtureScript writes fixtureName into dir and returns a
// fake-pi script that replays it for any invocation. The fixtures carry
// the session header's cwd as the __CWD__ placeholder, which the script
// substitutes with the process's own working directory, because the
// adapter compares the header's cwd against the turn's workspace.
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
	return writePiScript(t, dir, `sed "s|__CWD__|$PWD|g" '`+runPath+`'`)
}

// eventsOfType returns every event of the given type.
func eventsOfType(events []domain.AgentEvent, typ domain.AgentEventType) []domain.AgentEvent {
	var out []domain.AgentEvent
	for _, e := range events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// notificationMessages returns the message of every notification event.
func notificationMessages(events []domain.AgentEvent) []string {
	var out []string
	for _, e := range eventsOfType(events, domain.EventNotification) {
		out = append(out, e.Message)
	}
	return out
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

	// One notification per assistant response: the deltas, the
	// text_end, the message_end and the turn_end all describe the same
	// response, and exactly one of them may reach the orchestrator.
	got := notificationMessages(events)
	if len(got) != 1 || got[0] != "Hello, world!" {
		t.Errorf("notifications = %v, want exactly [\"Hello, world!\"]", got)
	}
	if len(eventsOfType(events, domain.EventMalformed)) != 0 {
		t.Errorf("malformed events = %+v, want none", eventsOfType(events, domain.EventMalformed))
	}
	if result.Usage.InputTokens != 110 || result.Usage.OutputTokens != 20 || result.Usage.CacheReadTokens != 10 {
		t.Errorf("usage = %+v, want input 110 output 20 cacheRead 10", result.Usage)
	}
	if err := a.StopSession(context.Background(), session); err != nil {
		t.Fatalf("StopSession() error = %v", err)
	}
}

// TestRunTurn_ReasoningStaysOutOfOutput pins that pi's reasoning blocks
// are internal deliberation: they never become a notification, and their
// tokens are never added to the response's output a second time.
func TestRunTurn_ReasoningStaysOutOfOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, "thinking_turn.jsonl"))
	events, result, err := collectEvents(t, a, session, "greet me")
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}

	got := notificationMessages(events)
	if len(got) != 1 || got[0] != "Hi." {
		t.Errorf("notifications = %v, want exactly [\"Hi.\"]: thinking must not become assistant text", got)
	}
	// pi reports output 40 including 30 reasoning tokens; adding
	// reasoning again would report 70.
	if result.Usage.OutputTokens != 40 {
		t.Errorf("OutputTokens = %d, want 40: pi's output already includes reasoning", result.Usage.OutputTokens)
	}
	if result.Usage.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300", result.Usage.InputTokens)
	}
}

// TestRunTurn_ToolResultPerExecution asserts one tool result per
// tool_execution_end, carrying that event's top-level isError flag, and
// no duplicate from the toolcall deltas that precede it.
func TestRunTurn_ToolResultPerExecution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		fixture      string
		wantTool     string
		wantToolErr  bool
		wantTexts    []string
		wantUsageSum domain.TokenUsage
	}{
		{
			fixture:     "tool_success.jsonl",
			wantTool:    "write",
			wantToolErr: false,
			wantTexts:   []string{"done"},
			// Two model responses: 50+10 then 40+8.
			wantUsageSum: domain.TokenUsage{InputTokens: 90, OutputTokens: 18, TotalTokens: 108},
		},
		{
			fixture:      "tool_error.jsonl",
			wantTool:     "bash",
			wantToolErr:  true,
			wantTexts:    []string{"the command failed"},
			wantUsageSum: domain.TokenUsage{InputTokens: 95, OutputTokens: 22, TotalTokens: 117},
		},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			a, err := NewPiAdapter(map[string]any{})
			if err != nil {
				t.Fatalf("NewPiAdapter() error = %v", err)
			}
			session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, tt.fixture))
			events, result, err := collectEvents(t, a, session, "work")
			if err != nil {
				t.Fatalf("RunTurn() error = %v", err)
			}

			tools := eventsOfType(events, domain.EventToolResult)
			if len(tools) != 1 {
				t.Fatalf("tool results = %+v, want exactly 1", tools)
			}
			if tools[0].ToolName != tt.wantTool || tools[0].ToolError != tt.wantToolErr {
				t.Errorf("tool result = %+v, want name %q ToolError %t", tools[0], tt.wantTool, tt.wantToolErr)
			}

			got := notificationMessages(events)
			if strings.Join(got, "|") != strings.Join(tt.wantTexts, "|") {
				t.Errorf("notifications = %v, want %v", got, tt.wantTexts)
			}
			if result.Usage != tt.wantUsageSum {
				t.Errorf("usage = %+v, want %+v: every model response in the turn is summed once", result.Usage, tt.wantUsageSum)
			}
		})
	}
}

// TestRunTurn_AssistantErrorAbortsExitZero pins the exit-code gap the
// 0.85.1 CLI has: --mode json applies no stop-reason check, so a
// provider failure and an abort both exit 0. Neither may be recorded as
// a successful turn, and the failure's stderr reaches the terminal
// report as a bounded excerpt.
func TestRunTurn_AssistantErrorAbortsExitZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		fixture   string
		wantInMsg string
		wantTexts []string
	}{
		{fixture: "assistant_error.jsonl", wantInMsg: "upstream 529 overloaded", wantTexts: []string{"I will start "}},
		{fixture: "assistant_aborted.jsonl", wantInMsg: "aborted"},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			fixture := filepath.Join(dir, tt.fixture)
			data, err := os.ReadFile(filepath.Join("testdata", tt.fixture))
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if err := os.WriteFile(fixture, data, 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			script := writePiScript(t, dir, `echo "request failed: upstream said no" >&2
sed "s|__CWD__|$PWD|g" '`+fixture+`'`)

			a, err := NewPiAdapter(map[string]any{})
			if err != nil {
				t.Fatalf("NewPiAdapter() error = %v", err)
			}
			session := mustStartSession(t, a, dir, script)
			events, result, err := collectEvents(t, a, session, "work")

			if err == nil {
				t.Fatal("RunTurn() error = nil, want a failure: pi --mode json exits 0 for a failed response")
			}
			agentErr, ok := err.(*domain.AgentError)
			if !ok {
				t.Fatalf("error type = %T, want *domain.AgentError", err)
			}
			if agentErr.Kind != domain.ErrResponseError {
				t.Errorf("Kind = %q, want %q", agentErr.Kind, domain.ErrResponseError)
			}
			if !strings.Contains(agentErr.Message, tt.wantInMsg) {
				t.Errorf("message = %q, want it to contain %q", agentErr.Message, tt.wantInMsg)
			}
			if !strings.Contains(agentErr.Message, "request failed: upstream said no") {
				t.Errorf("message = %q, want the bounded stderr excerpt in the terminal failure evidence", agentErr.Message)
			}
			if result.ExitReason != domain.EventTurnFailed {
				t.Errorf("ExitReason = %q, want %q", result.ExitReason, domain.EventTurnFailed)
			}
			// The partial text is reported once, as the response's own
			// completed text, before the failure is recorded.
			if got := notificationMessages(events); strings.Join(got, "|") != strings.Join(tt.wantTexts, "|") {
				t.Errorf("notifications = %v, want %v", got, tt.wantTexts)
			}
		})
	}
}

// TestRunTurn_ExitZeroNoWorkIsFailure pins the shared zero-exit,
// no-success rule for a pi run that produced no assistant output and no
// tool activity at all.
func TestRunTurn_ExitZeroNoWorkIsFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, "framing_only.jsonl"))
	_, result, err := collectEvents(t, a, session, "work")

	if err == nil {
		t.Fatal("RunTurn() error = nil, want a zero-work failure")
	}
	agentcoreT := agentcore.TurnEvidence{ExitObserved: true, Work: agentcore.WorkAbsent}
	if result.ExitReason != agentcore.DecideTurn(agentcoreT).ExitReason {
		t.Errorf("ExitReason = %q, want the shared zero-work row %q", result.ExitReason, agentcore.DecideTurn(agentcoreT).ExitReason)
	}
}

// TestRunTurn_FramingEventsProduceNothing asserts that the event families
// carrying no content are recognized rather than dropped: an
// unrecognized type is a malformed line, so a framing event that arrived
// without being reported proves the two are distinguishable.
func TestRunTurn_FramingEventsProduceNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, "framing_only.jsonl"))
	events, result, _ := collectEvents(t, a, session, "work")

	if got := eventsOfType(events, domain.EventMalformed); len(got) != 0 {
		t.Errorf("malformed events = %+v, want none: every framing type is part of the 0.85.1 schema", got)
	}
	if got := eventsOfType(events, domain.EventToolResult); len(got) != 0 {
		t.Errorf("tool results = %+v, want none: this fixture has no tool_execution_end", got)
	}
	// No assistant message_end and no turn_end, so no assistant output.
	if result.ExitReason != domain.EventTurnFailed {
		t.Errorf("ExitReason = %q, want %q", result.ExitReason, domain.EventTurnFailed)
	}
}

// TestRunTurn_MalformedLinesAreReported asserts that every fault class
// the 0.85.1 stream can carry reaches the orchestrator as a malformed
// event rather than as a silently successful turn: an undecodable
// nested payload, a terminal record with no role, an unknown event type,
// an unknown delta variant, and a non-object line.
func TestRunTurn_MalformedLinesAreReported(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, "malformed_turn.jsonl"))
	events, _, err := collectEvents(t, a, session, "work")
	if err == nil {
		t.Fatal("RunTurn() error = nil, want the no-work failure after only malformed lines")
	}
	if agentErr, ok := err.(*domain.AgentError); !ok || agentErr.Kind != domain.ErrTurnFailed {
		t.Fatalf("RunTurn() error = %v, want a turn failure after only malformed lines", err)
	}

	// malformed_turn.jsonl carries one bad line per fault class: an
	// undecodable turn_end message, a message with no role, a turn_end
	// with no message at all, a message whose content is not a list, two
	// bad message_updates, a tool_execution_end with no toolName, an
	// undecodable compaction result, and an unknown event type.
	malformed := eventsOfType(events, domain.EventMalformed)
	if len(malformed) != 10 {
		t.Errorf("malformed count = %d, want 10: %+v", len(malformed), malformed)
	}
	for _, want := range []string{
		`event "turn_end" message`,
		"carries no role",
		"missing payload",
		`event "message_end" message`,
		"assistantMessageEvent",
		"carries no toolName",
		"compaction_end result",
		"unknown pi event type \"future_event\"",
	} {
		found := false
		for _, e := range malformed {
			if strings.Contains(e.Message, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no malformed event mentions %q: %+v", want, malformed)
		}
	}
}

func TestRunTurn_NonObjectLineIsMalformed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	script := writePiScript(t, dir, `printf 'pi: warming up\n'
printf '{"type":"session","version":3,"id":"s1","timestamp":"t","cwd":"%s"}\n' "$PWD"`)
	session := mustStartSession(t, a, dir, script)
	events, _, _ := collectEvents(t, a, session, "work")

	malformed := eventsOfType(events, domain.EventMalformed)
	if len(malformed) != 1 || !strings.Contains(malformed[0].Message, "warming up") {
		t.Errorf("malformed = %+v, want exactly one carrying the non-JSON line", malformed)
	}
}

// TestRunTurn_CompactionAccounting asserts that a compaction's model call
// is counted once, that a failed compaction is surfaced, and that a
// later successful retry still settles.
func TestRunTurn_CompactionAccounting(t *testing.T) {
	t.Parallel()

	t.Run("usage counted once", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		a, err := NewPiAdapter(map[string]any{})
		if err != nil {
			t.Fatalf("NewPiAdapter() error = %v", err)
		}
		session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, "compaction_turn.jsonl"))
		_, result, err := collectEvents(t, a, session, "work")
		if err != nil {
			t.Fatalf("RunTurn() error = %v", err)
		}

		// 50+10 from the tool call, 9000+800 from the compaction, 40+9
		// from the final response.
		want := domain.TokenUsage{InputTokens: 9090, OutputTokens: 819, TotalTokens: 9909}
		if result.Usage != want {
			t.Errorf("usage = %+v, want %+v", result.Usage, want)
		}
	})

	t.Run("failure surfaced and retry settles", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		a, err := NewPiAdapter(map[string]any{})
		if err != nil {
			t.Fatalf("NewPiAdapter() error = %v", err)
		}
		session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, "compaction_retry.jsonl"))
		events, result, err := collectEvents(t, a, session, "work")
		if err != nil {
			t.Fatalf("RunTurn() error = %v", err)
		}

		var surfaced bool
		for _, msg := range notificationMessages(events) {
			if strings.Contains(msg, "summarizer timed out") {
				surfaced = true
			}
		}
		if !surfaced {
			t.Errorf("notifications = %v, want the compaction failure surfaced", notificationMessages(events))
		}

		// 40+9 from the response, 8000+700 from the successful retry.
		// The failed attempt contributed no usage, so it is not counted.
		want := domain.TokenUsage{InputTokens: 8040, OutputTokens: 709, TotalTokens: 8749}
		if result.Usage != want {
			t.Errorf("usage = %+v, want %+v", result.Usage, want)
		}
	})
}

// TestRunTurn_MixedModelAttribution pins the deterministic rule for a
// turn whose responses came from more than one model: the usage event
// names the last counted response's model, and both responses'
// usage is summed.
func TestRunTurn_MixedModelAttribution(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := writePiScript(t, dir, `cat <<EOF
{"type":"session","version":3,"id":"mix-1","timestamp":"t","cwd":"$PWD"}
{"type":"turn_start"}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"first"}],"api":"anthropic-messages","provider":"anthropic","model":"model-one","usage":{"input":10,"output":2,"cacheRead":0,"cacheWrite":0,"totalTokens":12},"stopReason":"stop"}}
{"type":"turn_end","message":{"role":"assistant","content":[{"type":"text","text":"first"}],"api":"anthropic-messages","provider":"anthropic","model":"model-one","usage":{"input":10,"output":2,"cacheRead":0,"cacheWrite":0,"totalTokens":12},"stopReason":"stop"},"toolResults":[]}
{"type":"turn_start"}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"second"}],"api":"openai-responses","provider":"openai","model":"model-two","usage":{"input":20,"output":3,"cacheRead":0,"cacheWrite":0,"totalTokens":23},"stopReason":"stop"}}
{"type":"turn_end","message":{"role":"assistant","content":[{"type":"text","text":"second"}],"api":"openai-responses","provider":"openai","model":"model-two","usage":{"input":20,"output":3,"cacheRead":0,"cacheWrite":0,"totalTokens":23},"stopReason":"stop"},"toolResults":[]}
EOF`)
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, script)
	events, result, err := collectEvents(t, a, session, "work")
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}

	want := domain.TokenUsage{InputTokens: 30, OutputTokens: 5, TotalTokens: 35}
	if result.Usage != want {
		t.Errorf("usage = %+v, want %+v", result.Usage, want)
	}
	agenttest.AssertModelReported(t, events, "model-two")
}

// TestRunTurn_UsageAccumulatesAcrossTurns asserts the session's
// run-cumulative figure grows monotonically across successive turns.
func TestRunTurn_UsageAccumulatesAcrossTurns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, "simple_turn.jsonl"))

	first, resultOne, err := collectEvents(t, a, session, "one")
	if err != nil {
		t.Fatalf("first RunTurn() error = %v", err)
	}
	second, resultTwo, err := collectEvents(t, a, session, "two")
	if err != nil {
		t.Fatalf("second RunTurn() error = %v", err)
	}

	agenttest.AssertUsageContract(t, append(append([]domain.AgentEvent{}, first...), second...))
	if resultOne.Usage.InputTokens != 110 {
		t.Errorf("first usage = %+v, want input 110", resultOne.Usage)
	}
	if resultTwo.Usage.InputTokens != 220 || resultTwo.Usage.OutputTokens != 40 {
		t.Errorf("second usage = %+v, want the session total input 220 output 40", resultTwo.Usage)
	}
	if resultTwo.Usage.TotalTokens != 260 {
		t.Errorf("second TotalTokens = %d, want 260", resultTwo.Usage.TotalTokens)
	}
}

// TestRunTurn_ResumeSession resumes into a session whose header names
// the id Sortie already held, and reports session_started once.
func TestRunTurn_ResumeSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session, err := a.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath:   dir,
		AgentConfig:     domain.AgentConfig{Command: writeRunFixtureScript(t, dir, "resume_turn.jsonl")},
		ResumeSessionID: "7a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	events, _, err := collectEvents(t, a, session, "continue")
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}

	started := eventsOfType(events, domain.EventSessionStarted)
	if len(started) != 1 {
		t.Fatalf("session_started count = %d, want 1: %+v", len(started), started)
	}
	if started[0].SessionID != "7a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d" {
		t.Errorf("session_started SessionID = %q, want the resumed id", started[0].SessionID)
	}
	if got := notificationMessages(events); len(got) != 1 || got[0] != "resumed" {
		t.Errorf("notifications = %v, want [\"resumed\"]", got)
	}
}

// TestAssertUsageReporting proves pi's registered usage-reporting
// declaration (turn_end, per_model) against a real event stream: one
// cumulative usage figure, emitted after the turn's work, carrying the
// model that produced it.
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
	agenttest.AssertModelReported(t, events, "claude-sonnet-4-5")
}

func TestRunTurn_NonZeroExitFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	script := writePiScript(t, dir, `printf '{"type":"session","version":3,"id":"s1","timestamp":"t","cwd":"%s"}\n' "$PWD"; exit 3`)
	session := mustStartSession(t, a, dir, script)
	_, result, err := collectEvents(t, a, session, "do work")
	if err == nil {
		t.Fatal("RunTurn() error = nil, want port_exit failure")
	}
	if result.ExitReason != domain.EventTurnFailed {
		t.Errorf("ExitReason = %q, want %q", result.ExitReason, domain.EventTurnFailed)
	}
}

// writeInlineRunScript writes lines as a run stream in dir and returns a
// fake-pi that replays it, with the session header's __CWD__ placeholder
// resolved to the process's own working directory.
func writeInlineRunScript(t *testing.T, dir string, lines ...string) string {
	t.Helper()

	path := filepath.Join(dir, "stream.jsonl")
	body := "{\"type\":\"session\",\"version\":3,\"id\":\"stream-1\",\"timestamp\":\"t\",\"cwd\":\"__CWD__\"}\n" +
		strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return writePiScript(t, dir, `sed "s|__CWD__|$PWD|g" '`+path+`'`)
}

// assistantMessage returns the completed-message JSON of one assistant
// response carrying text and a usage figure.
func assistantMessage(text string, input, output int) string {
	return fmt.Sprintf(`{"role":"assistant","content":[{"type":"text","text":%q}],`+
		`"api":"anthropic-messages","provider":"anthropic","model":"m1",`+
		`"usage":{"input":%d,"output":%d,"cacheRead":0,"cacheWrite":0},"stopReason":"stop"}`,
		text, input, output)
}

// TestRunTurn_EveryDeltaVariantIsRecognized walks the 0.85.1 delta set
// through a real stream. An unlisted variant is reported as a malformed
// line, so a variant the CLI does write but the adapter does not know
// fails here by name.
func TestRunTurn_EveryDeltaVariantIsRecognized(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	msg := assistantMessage("done", 30, 4)

	lines := []string{`{"type":"turn_start"}`}
	for _, variant := range slices.Sorted(maps.Keys(knownDeltaTypes)) {
		lines = append(lines, fmt.Sprintf(
			`{"type":"message_update","assistantMessageEvent":{"type":%q,"contentIndex":0,"delta":"x"}}`, variant))
	}
	lines = append(lines,
		`{"type":"message_end","message":`+msg+`}`,
		`{"type":"turn_end","message":`+msg+`,"toolResults":[]}`,
	)

	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, writeInlineRunScript(t, dir, lines...))
	events, result, err := collectEvents(t, a, session, "work")
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}

	if got := eventsOfType(events, domain.EventMalformed); len(got) != 0 {
		t.Errorf("malformed events = %+v, want none: every 0.85.1 delta variant is recognized", got)
	}
	if got := notificationMessages(events); len(got) != 1 || got[0] != "done" {
		t.Errorf("notifications = %v, want exactly [\"done\"]", got)
	}
	if result.Usage != (domain.TokenUsage{InputTokens: 30, OutputTokens: 4, TotalTokens: 34}) {
		t.Errorf("usage = %+v, want the response counted once", result.Usage)
	}
}

// TestRunTurn_ToolErrorComesFromTheTopLevelFlag pins which isError the
// tool result carries. The nested result object also carries one, and the
// two can disagree: the top-level flag is the CLI's verdict for the
// execution, and the nested one belongs to the tool's own output.
func TestRunTurn_ToolErrorComesFromTheTopLevelFlag(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	msg := assistantMessage("ran both", 40, 6)
	lines := []string{
		`{"type":"turn_start"}`,
		`{"type":"message_end","message":` + msg + `}`,
		`{"type":"tool_execution_end","toolCallId":"c1","toolName":"read","result":{"content":[],"isError":true}}`,
		`{"type":"tool_execution_end","toolCallId":"c2","toolName":"bash","result":{"content":[],"isError":false},"isError":true}`,
		`{"type":"turn_end","message":` + msg + `,"toolResults":[]}`,
	}

	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, writeInlineRunScript(t, dir, lines...))
	events, _, err := collectEvents(t, a, session, "work")
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}

	tools := eventsOfType(events, domain.EventToolResult)
	if len(tools) != 2 {
		t.Fatalf("tool results = %+v, want 2", tools)
	}
	if tools[0].ToolName != "read" || tools[0].ToolError {
		t.Errorf("first tool result = %+v, want name %q with ToolError false: the nested result's flag is not the verdict", tools[0], "read")
	}
	if tools[1].ToolName != "bash" || !tools[1].ToolError {
		t.Errorf("second tool result = %+v, want name %q with ToolError true", tools[1], "bash")
	}
}

// TestRunTurn_TurnEndSuppliesTheMissingMessage covers the fallback: a
// stream that delivered no message_end still reports the assistant
// response, because turn_end names the same response and is the only
// place its usage appears.
func TestRunTurn_TurnEndSuppliesTheMissingMessage(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	msg := assistantMessage("only turn_end", 12, 3)
	script := writeInlineRunScript(t, dir,
		`{"type":"turn_start"}`,
		`{"type":"turn_end","message":`+msg+`,"toolResults":[]}`,
	)

	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, script)
	events, result, err := collectEvents(t, a, session, "work")
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}

	if got := notificationMessages(events); len(got) != 1 || got[0] != "only turn_end" {
		t.Errorf("notifications = %v, want exactly [\"only turn_end\"]", got)
	}
	if result.Usage != (domain.TokenUsage{InputTokens: 12, OutputTokens: 3, TotalTokens: 15}) {
		t.Errorf("usage = %+v, want the turn_end figure settled", result.Usage)
	}
}

// TestRunTurn_AgentEndRepeatsNeitherTextNorUsage pins that agent_end,
// which replays every message of the run, contributes nothing: the text
// has already been reported and the usage already counted, so reading it
// would report the response twice and bill it twice.
func TestRunTurn_AgentEndRepeatsNeitherTextNorUsage(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := assistantMessage("once", 10, 2)
	second := assistantMessage("twice", 20, 4)
	lines := []string{
		`{"type":"turn_start"}`,
		`{"type":"message_end","message":` + first + `}`,
		`{"type":"turn_end","message":` + first + `,"toolResults":[]}`,
		`{"type":"turn_start"}`,
		`{"type":"message_end","message":` + second + `}`,
		`{"type":"turn_end","message":` + second + `,"toolResults":[]}`,
		`{"type":"agent_end","messages":[` + first + `,` + second + `],"willRetry":false}`,
	}

	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, writeInlineRunScript(t, dir, lines...))
	events, result, err := collectEvents(t, a, session, "work")
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}

	want := []string{"once", "twice"}
	if got := notificationMessages(events); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("notifications = %v, want %v: agent_end replays the same messages", got, want)
	}
	// 10+2 and 20+4 counted once each; agent_end's copies add nothing.
	if result.Usage != (domain.TokenUsage{InputTokens: 30, OutputTokens: 6, TotalTokens: 36}) {
		t.Errorf("usage = %+v, want the two responses counted once each", result.Usage)
	}
}
