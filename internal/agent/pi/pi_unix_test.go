//go:build unix

// Session lifecycle: the session header, the resume identity, session
// teardown, and a turn ended by cancellation. The tests run a real
// subprocess, so they are bounded by the platform's process-group
// termination and signal semantics.

package pi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/agent/agenttest/dispositiontest"
	"github.com/sortie-ai/sortie/internal/domain"
)

// turnOutcome is one completed RunTurn's result and error.
type turnOutcome struct {
	result domain.TurnResult
	err    error
}

// testDrainGrace bounds every post-exit wait a turn performs on its
// readers, so a test that ends a turn by signal does not spend the
// production default on it.
const testDrainGrace = 250 * time.Millisecond

// boundDrain sets the session's post-exit drain bound. It must be called
// before the session's first turn, while no turn is running.
func boundDrain(session domain.Session) {
	session.Internal.(*sessionState).drainGrace = testDrainGrace
}

// headerScript returns a fake-pi whose only output is one session header
// with the given id and the cwd the shell expression cwdExpr expands to.
func headerScript(t *testing.T, dir, id, cwdExpr string) string {
	t.Helper()
	return writePiScript(t, dir, fmt.Sprintf(
		`printf '{"type":"session","version":3,"id":%q,"timestamp":"t","cwd":"%%s"}\n' %s`, id, cwdExpr))
}

// hangingTurnScript returns a fake-pi that reports a session header and
// one assistant delta, then holds the turn open until it is signalled.
// The single delta is what the caller waits for before acting, so no
// assertion races the subprocess's startup.
func hangingTurnScript(t *testing.T, dir string) string {
	t.Helper()
	return writePiScript(t, dir, `printf '{"type":"session","version":3,"id":"hang-1","timestamp":"t","cwd":"%s"}\n' "$PWD"
printf '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"working"}}\n'
while :; do sleep 1; done`)
}

// startTurn runs one turn in the background and returns a channel of its
// first event and one of its outcome, so a test can act while the turn
// is still open.
func startTurn(t *testing.T, ctx context.Context, a domain.AgentAdapter, session domain.Session) (<-chan domain.AgentEvent, <-chan turnOutcome) {
	t.Helper()

	first := make(chan domain.AgentEvent, 1)
	out := make(chan turnOutcome, 1)
	go func() {
		result, err := a.RunTurn(ctx, session, domain.RunTurnParams{
			Prompt: "work",
			OnEvent: func(e domain.AgentEvent) {
				select {
				case first <- e:
				default:
				}
			},
		})
		out <- turnOutcome{result: result, err: err}
	}()
	return first, out
}

// awaitFirstEvent blocks until the turn reports its first event, or
// fails t when the test deadline passes first.
func awaitFirstEvent(t *testing.T, first <-chan domain.AgentEvent, deadline <-chan struct{}) {
	t.Helper()
	select {
	case <-first:
	case <-deadline:
		t.Fatal("timed out waiting for the turn's first event")
	}
}

// awaitOutcome blocks until the turn returns, or fails t when the test
// deadline passes first.
func awaitOutcome(t *testing.T, out <-chan turnOutcome, deadline <-chan struct{}) turnOutcome {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-deadline:
		t.Fatal("RunTurn did not return inside the test deadline")
		return turnOutcome{}
	}
}

// TestRunTurn_SessionHeaderWorkspaceMismatch pins the header's cwd
// against the turn's own workspace: a pi session that runs somewhere else
// is a different session than the one Sortie launched, and adopting its
// identity would attach this turn's events to a workspace the agent never
// worked in.
func TestRunTurn_SessionHeaderWorkspaceMismatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, headerScript(t, dir, "ws-1", `"$PWD/elsewhere"`))
	boundDrain(session)

	events, result, runErr := collectEvents(t, a, session, "work")
	if runErr == nil {
		t.Fatal("RunTurn() error = nil, want the workspace mismatch to fail the turn")
	}
	if got := eventsOfType(events, domain.EventSessionStarted); len(got) != 0 {
		t.Errorf("session_started = %+v, want none: the header was refused", got)
	}
	if got := eventsOfType(events, domain.EventMalformed); len(got) != 0 {
		t.Errorf("malformed events = %+v, want none: a mismatched header is a turn failure, not an undecodable line", got)
	}

	want := fmt.Sprintf("session %q runs in %q, want the turn's workspace %q", "ws-1", filepath.Join(dir, "elsewhere"), dir)
	dispositiontest.AssertDispositionContract(t, agentcore.TurnEvidence{
		Terminal:          agentcore.TerminalFailure,
		TerminalErrorKind: domain.ErrResponseError,
		TerminalMessage:   want,
	}, result, runErr)
}

// TestRunTurn_SessionIDMismatch pins the resume identity: a header naming
// a session other than the one being resumed is refused, so a turn never
// appends to, or reports usage for, a pi session Sortie did not launch.
func TestRunTurn_SessionIDMismatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session, err := a.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath:   dir,
		AgentConfig:     domain.AgentConfig{Command: headerScript(t, dir, "actual-id", `"$PWD"`)},
		ResumeSessionID: "expected-id",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	boundDrain(session)

	events, result, runErr := collectEvents(t, a, session, "work")
	if runErr == nil {
		t.Fatal("RunTurn() error = nil, want the id mismatch to fail the turn")
	}
	if got := eventsOfType(events, domain.EventSessionStarted); len(got) != 0 {
		t.Errorf("session_started = %+v, want none: the mismatched header was refused", got)
	}
	if result.SessionID != "expected-id" {
		t.Errorf("result SessionID = %q, want the id Sortie resumed into", result.SessionID)
	}

	dispositiontest.AssertDispositionContract(t, agentcore.TurnEvidence{
		Terminal:          agentcore.TerminalFailure,
		TerminalErrorKind: domain.ErrResponseError,
		TerminalMessage:   `session id mismatch: expected "expected-id", got "actual-id"`,
	}, result, runErr)
}

// TestRunTurn_SessionStartedOnceAcrossTurns asserts the header is adopted
// once for a session, however many turns carry it: every turn after the
// first re-reports the same pi session, and a second session_started
// would tell the orchestrator a second session exists.
func TestRunTurn_SessionStartedOnceAcrossTurns(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, "simple_turn.jsonl"))
	boundDrain(session)

	first, _, err := collectEvents(t, a, session, "one")
	if err != nil {
		t.Fatalf("first RunTurn() error = %v", err)
	}
	second, _, err := collectEvents(t, a, session, "two")
	if err != nil {
		t.Fatalf("second RunTurn() error = %v", err)
	}

	started := eventsOfType(append(first, second...), domain.EventSessionStarted)
	if len(started) != 1 {
		t.Fatalf("session_started count = %d, want 1: %+v", len(started), started)
	}
	if started[0].SessionID != "6f1b0c2a-1d3e-4a5b-8c9d-0e1f2a3b4c5d" {
		t.Errorf("session_started SessionID = %q, want the id the header named", started[0].SessionID)
	}
}

// TestRunTurn_ResumeForwardsTheSessionID captures the argv the process
// actually received, which is where --session reaches pi. Asserting on
// the built argument slice would not show that the resumed id survived
// the launch.
func TestRunTurn_ResumeForwardsTheSessionID(t *testing.T) {
	t.Parallel()

	const resumeID = "7a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv.txt")
	script := writePiScript(t, dir, `printf '%s\n' "$@" > `+argvPath+`
exit 0`)

	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session, err := a.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath:   dir,
		AgentConfig:     domain.AgentConfig{Command: script},
		ResumeSessionID: resumeID,
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	boundDrain(session)

	// The fake process writes its argv and exits without a JSON
	// response, so the shared no-work rule fails this turn. The captured
	// launch is the subject.
	_, _, _ = collectEvents(t, a, session, "work")

	argv, err := readLines(argvPath)
	if err != nil {
		t.Fatalf("ReadFile(argv) error = %v", err)
	}
	if !hasConsecutive(argv, "--session", resumeID) {
		t.Errorf("argv = %v, want --session %s forwarded to the pi process", argv, resumeID)
	}
}

// TestStopSession_NoActiveTurn asserts a stop on a session that is
// between turns, or is stopped twice, succeeds: the orchestrator calls
// stop on every path out of a session, and a teardown with nothing to
// tear down is not an error.
func TestStopSession_NoActiveTurn(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, headerScript(t, dir, "stop-1", `"$PWD"`))

	if err := a.StopSession(context.Background(), session); err != nil {
		t.Fatalf("StopSession() error = %v, want nil with no turn running", err)
	}
	if err := a.StopSession(context.Background(), session); err != nil {
		t.Errorf("StopSession() second call error = %v, want nil", err)
	}
}

// TestRunTurn_ContextCancellationEndsTheTurn asserts a cancelled context
// ends the turn as cancelled and reaps the subprocess, rather than
// leaving RunTurn blocked on a process that never exits on its own.
func TestRunTurn_ContextCancellationEndsTheTurn(t *testing.T) {
	t.Parallel()

	testCtx, cancelTest := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelTest()

	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, hangingTurnScript(t, dir))
	boundDrain(session)

	turnCtx, cancelTurn := context.WithCancel(testCtx)
	first, out := startTurn(t, turnCtx, a, session)
	awaitFirstEvent(t, first, testCtx.Done())
	cancelTurn()

	got := awaitOutcome(t, out, testCtx.Done())
	dispositiontest.AssertDispositionContract(t, agentcore.TurnEvidence{
		Terminal:        agentcore.TerminalCancelled,
		TerminalMessage: "turn cancelled",
	}, got.result, got.err)
}

// TestStopSession_DuringTurnCancelsTheTurn asserts a stop issued while a
// turn is open signals the process group, waits for the exit, and reports
// the still-open turn as cancelled: the turn is closed by the teardown,
// not recorded as a turn that ended by itself.
func TestStopSession_DuringTurnCancelsTheTurn(t *testing.T) {
	t.Parallel()

	testCtx, cancelTest := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelTest()

	dir := t.TempDir()
	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, hangingTurnScript(t, dir))
	boundDrain(session)

	first, out := startTurn(t, testCtx, a, session)
	awaitFirstEvent(t, first, testCtx.Done())

	stopCtx, cancelStop := context.WithTimeout(testCtx, 5*time.Second)
	defer cancelStop()
	if err := a.StopSession(stopCtx, session); err != nil {
		t.Fatalf("StopSession() error = %v, want nil: the signalled process exits on its own", err)
	}

	got := awaitOutcome(t, out, testCtx.Done())
	dispositiontest.AssertDispositionContract(t, agentcore.TurnEvidence{
		Terminal:        agentcore.TerminalCancelled,
		TerminalMessage: "turn cancelled",
	}, got.result, got.err)
}

// TestRunTurn_EscapedDescendantWritingIsCapped drives the post-exit
// drain: a pi process that exits while a descendant it left behind keeps
// writing holds the reader's channel ready, so the turn cannot finish on
// the channel closing alone. The post-exit bound is the only thing that
// ends that wait, and it is one deadline rather than a fresh grace per
// line, so a descendant that keeps talking cannot spend more than one.
// The descendant is setsid-detached because an in-group one is released
// by the reaper's group kill, which ends the wait without reaching it.
func TestRunTurn_EscapedDescendantWritingIsCapped(t *testing.T) {
	agenttest.RequireSetsid(t)
	t.Parallel()

	testCtx, cancelTest := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelTest()

	dir := t.TempDir()
	// The descendant outlives the process Sortie launched and the group
	// it killed, so the test tears it down by pid.
	descendant := filepath.Join(dir, "descendant.pid")
	killDescendantOnCleanup(t, descendant)

	// The descendant writes a framing event, which is not model-authored
	// content and so is not work evidence: the turn's disposition then
	// rests on the exit code alone, the way a turn with no completed
	// response does. The line lives in a file the descendant cats, so
	// the JSON needs no quoting inside the nested sh -c. Waiting on the
	// descendant's own marker rather than on $! is what keeps the group
	// kill from racing its escape.
	chatter := filepath.Join(dir, "chatter.jsonl")
	if err := os.WriteFile(chatter, []byte(`{"type":"thinking_level_changed","level":"off"}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	script := writePiScript(t, dir, fmt.Sprintf(`printf '{"type":"session","version":3,"id":"orphan-1","timestamp":"t","cwd":"%%s"}\n' "$PWD"
setsid sh -c 'echo $$ > %[1]s; while :; do cat %[2]s; sleep 0.05; done' &
while [ ! -s %[1]s ]; do sleep 0.01; done
exit 0
`, descendant, chatter))

	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session := mustStartSession(t, a, dir, script)
	boundDrain(session)

	start := time.Now()
	_, out := startTurn(t, testCtx, a, session)
	got := awaitOutcome(t, out, testCtx.Done())
	elapsed := time.Since(start)

	// Without the cap the descendant holds the turn open indefinitely,
	// so the bound is the assertion: a turn that waits it out is the
	// failure this test exists to catch.
	if elapsed > 5*time.Second {
		t.Errorf("RunTurn() took %v, want the post-exit cap to end the wait", elapsed)
	}
	if got.result.ExitReason != domain.EventTurnFailed {
		t.Errorf("ExitReason = %q, want %q: no response was written", got.result.ExitReason, domain.EventTurnFailed)
	}
}

// killDescendantOnCleanup kills the process group led by the PID in path
// once the test ends. The adapter reaps the group it launched; a
// setsid-detached descendant is the test's to clean up.
func killDescendantOnCleanup(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() {
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if convErr != nil || pid <= 1 {
			return
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	})
}

// readLines returns path's lines with the trailing empty element removed.
func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n"), nil
}

// hasConsecutive reports whether value follows flag as consecutive
// elements of args.
func hasConsecutive(args []string, flag, value string) bool {
	for i := range len(args) - 1 {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
