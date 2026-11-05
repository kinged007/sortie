//go:build linux

package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/agent/procutil"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/waittest"
)

// pgidLeaderParams parameterizes the pgid-family scenarios: it names the
// fake runtime executable a leader starts as its descendant, and the file
// the leader records the descendant's PID into for the test to poll.
type pgidLeaderParams struct {
	ChildPath string
	PIDFile   string
	Escape    bool
}

// pgidLeaderScenario spawns a background descendant that inherits the
// leader's process group (no Setsid), records its PID, emits a JSONL
// notification line, and hangs. It is the Go equivalent of a shell
// fixture running "sleep 3600 & ...; sleep 3600".
func pgidLeaderScenario(_ []string, p pgidLeaderParams) int {
	child := exec.Command(p.ChildPath) //nolint:gosec // p.ChildPath is a fake runtime under t.TempDir()
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(p.PIDFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(`{"type":"notification"}`)
	agenttest.Hang()
	return 0
}

// escapedPgidLeaderScenario spawns a descendant with Setsid so it leaves the
// leader's process group while still inheriting the leader's stdout handle,
// the Go equivalent of "setsid sh -c '...' &". Go performs the setsid() call
// as part of the fork/exec sequence before Start returns, so unlike the
// shell fixture this needs no separate readiness marker for the child's own
// PID: cmd.Process.Pid is already the detached session's PID.
func escapedPgidLeaderScenario(_ []string, p pgidLeaderParams) int {
	child := exec.Command(p.ChildPath) //nolint:gosec // p.ChildPath is a fake runtime under t.TempDir()
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(p.PIDFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(`{"type":"notification"}`)
	agenttest.Hang()
	return 0
}

// stderrOnlyLeaderScenario writes its own stderr line, starts a descendant
// whose stdout is discarded but whose stderr is the leader's own (so it
// keeps that pipe's write end open after the leader exits), records the
// descendant's PID, and returns immediately instead of blocking.
func stderrOnlyLeaderScenario(_ []string, p pgidLeaderParams) int {
	fmt.Fprintln(os.Stderr, "direct child stderr")
	child := exec.Command(p.ChildPath) //nolint:gosec // p.ChildPath is a fake runtime under t.TempDir()
	child.Stderr = os.Stderr
	if p.Escape {
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(p.PIDFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(`{"type":"notification"}`)
	return 0
}

// selfSignalScenario sends itself SIGTERM with no handler installed, so the
// default disposition terminates the process the way a signal delivered by
// another process would, and hangs as a fallback in the unreached case the
// signal does not take effect immediately.
func selfSignalScenario(_ []string, _ json.RawMessage) int {
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM) //nolint:errcheck // best-effort self-signal; the point is the default disposition
	agenttest.Hang()
	return 0
}

// overflowOnTerminateScenario writes one line, catches the graceful
// termination signal, and only then writes an unterminated line one byte
// longer than the stdout scanner accepts, so the scan fails after the turn's
// context is already done. The scanner reads exactly its limit before it
// fails, so the last byte fits in the pipe, the write completes, and the
// runtime exits on its own well inside the stop grace.
func overflowOnTerminateScenario(_ []string, _ json.RawMessage) int {
	terminate := make(chan os.Signal, 1)
	signal.Notify(terminate, syscall.SIGTERM)
	fmt.Println(`{"type":"notification"}`)
	<-terminate
	fmt.Print(strings.Repeat("x", procutil.DefaultScannerMaxSize+1))
	return 0
}

func init() {
	scenarios["pgidLeader"] = agenttest.Typed(pgidLeaderScenario)
	scenarios["escapedPgidLeader"] = agenttest.Typed(escapedPgidLeaderScenario)
	scenarios["stderrOnlyLeader"] = agenttest.Typed(stderrOnlyLeaderScenario)
	scenarios["selfSignal"] = selfSignalScenario
	scenarios["overflowOnTerminate"] = overflowOnTerminateScenario
}

func TestForkPerTurnSession_StopWhileRunningCancelsWhateverTheRuntimeExitsWith(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ready := filepath.Join(tmpDir, "ready")
	script := agenttest.FakeRuntime(t, tmpDir, "agent", "trap", trapParams{Marker: ready, ExitOnSignal: true, ExitCode: 5})
	target := newTestTarget(tmpDir, script)
	sess := NewForkPerTurnSession(target, hooksWithEarlyExitDisposition(), slog.Default(), 0)

	emit, events := sinkEvents()
	done := make(chan error, 1)
	go func() {
		_, err := sess.RunTurn(context.Background(), "p", emit)
		done <- err
	}()

	waittest.File(t, ready)
	if err := sess.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() = %v", err)
	}

	err := waittest.Receive(t, done, "RunTurn return after Stop")

	requireAgentError(t, err, domain.ErrTurnCancelled)
	if !hasEventType(*events, domain.EventTurnCancelled) {
		t.Errorf("EventTurnCancelled not emitted; got %v", *events)
	}
}

func awaitReaped(t *testing.T, pid string) {
	t.Helper()
	n, err := strconv.Atoi(pid)
	if err != nil {
		t.Fatalf("pid %q is not a number: %v", pid, err)
	}
	waittest.Until(t, func() error {
		killErr := syscall.Kill(n, 0)
		if errors.Is(killErr, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("process %d is not reaped: kill(0) = %v", n, killErr)
	})
}

func TestForkPerTurnSession_StopAfterOwnExitKeepsTheEarlyExitReport(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	script := agenttest.FakeRuntime(t, tmpDir, "agent", agenttest.OutputScenario, agenttest.Output{Stdout: "not valid JSON\n", ExitCode: 3})
	hooks := hooksWithEarlyExitDisposition()
	var sess *ForkPerTurnSession
	hooks.ParseLine = func(_ []byte, _ func(domain.AgentEvent), pid string, _ *TurnSpend) (any, error) {
		awaitReaped(t, pid)
		if err := sess.Stop(context.Background()); err != nil {
			t.Errorf("Stop() = %v", err)
		}
		return nil, errors.New("line is not valid JSON")
	}
	sess = NewForkPerTurnSession(newTestTarget(tmpDir, script), hooks, slog.Default(), 200)

	emit, _ := sinkEvents()
	_, err := sess.RunTurn(context.Background(), "p", emit)

	requireAgentError(t, err, domain.ErrPortExit)
	if _, ok := errors.AsType[*EarlyExitError](err); !ok {
		t.Errorf("RunTurn() error = %v, want a chain carrying an *EarlyExitError", err)
	}
}

func TestForkPerTurnSession_ZeroExitIsNotCancelledWhenTheDeadlineFallsDuringTheDrain(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "descendant.pid")
	script := writeStderrOnlyDescendantScript(t, tmpDir, pidFile, true)
	t.Cleanup(func() { agenttest.KillEscaped(pidFile) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hooks := noopHooks()
	hooks.ParseLine = func(_ []byte, _ func(domain.AgentEvent), pid string, _ *TurnSpend) (any, error) {
		awaitReaped(t, pid)
		cancel()
		return nil, nil
	}
	gotExitCode := -1
	hooks.OnFinalize = func(emit func(domain.AgentEvent), _ any, exitCode int, _ error, _ []string, _ *domain.AgentError, _ *TurnSpend) (domain.TurnResult, *domain.AgentError) {
		gotExitCode = exitCode
		EmitTurnCompleted(emit, "ok", 0, domain.TokenUsage{}, nil)
		return domain.TurnResult{ExitReason: domain.EventTurnCompleted}, nil
	}
	sess := NewForkPerTurnSession(newTestTarget(tmpDir, script), hooks, slog.Default(), 0)
	sess.drainGrace = 200 * time.Millisecond

	emit, _ := sinkEvents()
	result, err := sess.RunTurn(ctx, "p", emit)

	if err != nil {
		t.Fatalf("RunTurn() error = %v, want nil for a runtime that exited zero before the deadline", err)
	}
	if result.ExitReason != domain.EventTurnCompleted {
		t.Errorf("TurnResult.ExitReason = %q, want %q", result.ExitReason, domain.EventTurnCompleted)
	}
	if gotExitCode != 0 {
		t.Errorf("OnFinalize exit code = %d, want 0", gotExitCode)
	}
}

// writePgidScript builds a leader fake runtime that spawns a long-running
// grandchild in its own process group, writes the grandchild's PID to
// pidFile, and emits a JSONL notification line before hanging.
func writePgidScript(t *testing.T, dir, pidFile string) string {
	t.Helper()
	child := agenttest.FakeRuntime(t, dir, "agent-pgid-child", agenttest.OutputScenario, agenttest.Output{Hang: true})
	return agenttest.FakeRuntime(t, dir, "agent-pgid", "pgidLeader", pgidLeaderParams{ChildPath: child, PIDFile: pidFile})
}

// writeEscapedPgidScript builds a leader fake runtime that spawns a
// long-running grandchild via Setsid (so it leaves the process group while
// still inheriting the parent's stdout handle), writes the grandchild's PID
// to pidFile, and emits a JSONL notification line before hanging like
// writePgidScript's own leader.
func writeEscapedPgidScript(t *testing.T, dir, pidFile string) string {
	t.Helper()
	child := agenttest.FakeRuntime(t, dir, "agent-escaped-pgid-child", agenttest.OutputScenario, agenttest.Output{Hang: true})
	return agenttest.FakeRuntime(t, dir, "agent-escaped-pgid", "escapedPgidLeader", pgidLeaderParams{ChildPath: child, PIDFile: pidFile})
}

// writeStderrOnlyDescendantScript builds a leader fake runtime whose
// background descendant has its stdout discarded and inherits only the
// leader's stderr handle, and whose leader writes a stderr line of its own,
// the descendant PID, and the notification line, then exits normally
// instead of hanging.
func writeStderrOnlyDescendantScript(t *testing.T, dir, pidFile string, escape bool) string {
	t.Helper()
	child := agenttest.FakeRuntime(t, dir, "agent-stderr-only-child", agenttest.OutputScenario, agenttest.Output{Hang: true})
	return agenttest.FakeRuntime(t, dir, "agent-stderr-only-descendant", "stderrOnlyLeader", pgidLeaderParams{ChildPath: child, PIDFile: pidFile, Escape: escape})
}

// TestForkPerTurnSession_Arm5_ExternalSIGTERM verifies that a subprocess
// killed by a signal Sortie itself never sent, and which wrote nothing
// readable before dying, is classified as the early-exit report rather
// than a cancelled turn: today's turn_cancelled names an orchestrator
// cancellation that never happened, so the report corrects it. The turn
// still carries the session's usage snapshot and measurement verdict
// through that classification.
func TestForkPerTurnSession_Arm5_ExternalSIGTERM(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		measured bool
	}{
		{"MeasuredTrue", true},
		{"MeasuredFalse", false},
	} {
		measured := tc.measured
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			script := agenttest.FakeRuntime(t, tmpDir, "agent", "selfSignal", nil)
			target := newTestTarget(tmpDir, script)

			getUsage, calls := usageVerdictDouble(wantUsageVerdictSnapshot, measured)
			hooks := noopHooks()
			hooks.GetUsage = getUsage
			hooks.OnFinalize = func(emit func(domain.AgentEvent), _ any, exitCode int, _ error, _ []string, earlyExit *domain.AgentError, _ *TurnSpend) (domain.TurnResult, *domain.AgentError) {
				usage, _, measured := getUsage()
				return FinalizeTurn(emit, slog.Default(), TurnEvidence{
					ExitObserved: true,
					ExitCode:     exitCode,
					EarlyExit:    earlyExit,
				}, TurnMeta{Usage: usage, UsageMeasured: measured}, TurnSpend{})
			}
			sess := NewForkPerTurnSession(target, hooks, slog.Default(), 0)

			emit, events := sinkEvents()
			result, err := sess.RunTurn(context.Background(), "p", emit)

			assertUsageVerdictCarried(t, result, err, *events, domain.EventTurnFailed, domain.ErrPortExit, measured, *calls)

			var agentErr *domain.AgentError
			errors.As(err, &agentErr)
			var earlyExitErr *EarlyExitError
			if !errors.As(agentErr.Err, &earlyExitErr) {
				t.Fatalf("AgentError.Err = %v, want an *EarlyExitError", agentErr.Err)
			}
			if !strings.Contains(earlyExitErr.Status(), "signal") {
				t.Errorf("EarlyExitError.Status() = %q, want it to name the signal", earlyExitErr.Status())
			}
		})
	}
}

// TestForkPerTurnSession_UsageVerdict_ScanErrorAfterCancel verifies that a
// stdout scan that fails after the turn's context is cancelled reports a
// cancelled turn carrying the session's usage snapshot and measurement
// verdict.
func TestForkPerTurnSession_UsageVerdict_ScanErrorAfterCancel(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		measured bool
	}{
		{"MeasuredTrue", true},
		{"MeasuredFalse", false},
	} {
		measured := tc.measured
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			script := agenttest.FakeRuntime(t, tmpDir, "agent", "overflowOnTerminate", nil)
			target := newTestTarget(tmpDir, script)

			getUsage, calls := usageVerdictDouble(wantUsageVerdictSnapshot, measured)
			hooks := noopHooks()
			hooks.GetUsage = getUsage
			hooks.ParseLine = parseLineEmitSessionStartedOnce()
			sess := NewForkPerTurnSession(target, hooks, slog.Default(), 0)

			result, events, err := runWithSessionStartCancel(t, sess)

			assertUsageVerdictCarried(t, result, err, events, domain.EventTurnCancelled, domain.ErrTurnCancelled, measured, *calls)
		})
	}
}

// TestForkPerTurnSession_ProcessGroupIsolation verifies that cancelling the
// RunTurn context sends SIGTERM to the entire process group, killing both
// the agent subprocess and any grandchildren it spawned.
func TestForkPerTurnSession_ProcessGroupIsolation(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "grandchild.pid")
	script := writePgidScript(t, tmpDir, pidFile)

	target := newTestTarget(tmpDir, script)
	sess := NewForkPerTurnSession(target, noopHooks(), slog.Default(), 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emit, _ := sinkEvents()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.RunTurn(ctx, "p", emit) //nolint:errcheck // testing process group kill
	}()

	grandchildPID := waittest.PIDFile(t, pidFile)

	cancel()
	<-done

	waittest.ProcessGone(t, grandchildPID)
}

// TestForkPerTurnSession_DescendantHoldsStderrOnly verifies that a
// descendant which inherits only the stderr handle, and whose direct parent
// exits normally rather than blocking, no longer wedges RunTurn. Reaping the
// direct child releases the stderr drain on this platform, so the turn
// delivers the direct child's real stderr lines rather than
// procutil.AbandonedMarker, follows the subprocess's actual exit code
// instead of reporting a cancellation, and the surviving descendant is
// still cleaned up.
func TestForkPerTurnSession_DescendantHoldsStderrOnly(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "descendant.pid")
	script := writeStderrOnlyDescendantScript(t, tmpDir, pidFile, false)

	var gotStderrLines []string
	hooks := noopHooks()
	hooks.OnFinalize = func(emit func(domain.AgentEvent), lastParsed any, exitCode int, _ error, stderrLines []string, _ *domain.AgentError, _ *TurnSpend) (domain.TurnResult, *domain.AgentError) {
		gotStderrLines = stderrLines
		EmitTurnCompleted(emit, "ok", 0, domain.TokenUsage{}, nil)
		return domain.TurnResult{ExitReason: domain.EventTurnCompleted}, nil
	}

	target := newTestTarget(tmpDir, script)
	sess := NewForkPerTurnSession(target, hooks, slog.Default(), 0)
	sess.drainGrace = 200 * time.Millisecond

	emit, events := sinkEvents()
	type outcome struct {
		result domain.TurnResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := sess.RunTurn(context.Background(), "p", emit)
		done <- outcome{result, err}
	}()

	descendantPID := waittest.PIDFile(t, pidFile)

	got := waittest.Receive(t, done, "RunTurn outcome")

	if got.err != nil {
		t.Fatalf("RunTurn() error = %v, want nil", got.err)
	}
	if got.result.ExitReason != domain.EventTurnCompleted {
		t.Errorf("TurnResult.ExitReason = %q, want %q", got.result.ExitReason, domain.EventTurnCompleted)
	}
	if !hasEventType(*events, domain.EventTurnCompleted) {
		t.Errorf("EventTurnCompleted not emitted; got %v", *events)
	}

	wantStderrLines := []string{"direct child stderr"}
	if len(gotStderrLines) != len(wantStderrLines) || gotStderrLines[0] != wantStderrLines[0] {
		t.Errorf("OnFinalize stderrLines = %v, want %v", gotStderrLines, wantStderrLines)
	}

	waittest.ProcessGone(t, descendantPID)
}

// TestForkPerTurnSession_InGroupDescendantHoldsStdout drives
// writePgidScript's in-group descendant through a cancelled RunTurn and
// asserts that the descendant is dead once the turn returns and that the
// group kill releases the stdout reader before sess.drainGrace can fire,
// so no abandonment record is emitted. This is the unabandoned half of
// the comparison against
// TestForkPerTurnSession_EscapedDescendantHoldsStdout: both scripts spawn
// a descendant that inherits the standard-output handle and both turns
// are ended by the same context cancellation, so the two must end with
// an identical disposition and error.
func TestForkPerTurnSession_InGroupDescendantHoldsStdout(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "descendant.pid")
	script := writePgidScript(t, tmpDir, pidFile)

	spy := &agenttest.LogSpy{}
	target := newTestTarget(tmpDir, script)
	sess := NewForkPerTurnSession(target, noopHooks(), slog.New(spy), 0)
	sess.drainGrace = 200 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emit, events := sinkEvents()
	type outcome struct {
		result domain.TurnResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := sess.RunTurn(ctx, "p", emit)
		done <- outcome{result, err}
	}()

	descendantPID := waittest.PIDFile(t, pidFile)
	cancel()

	got := waittest.Receive(t, done, "RunTurn outcome")

	requireAgentError(t, got.err, domain.ErrTurnCancelled)
	if !hasEventType(*events, domain.EventTurnCancelled) {
		t.Errorf("EventTurnCancelled not emitted; got %v", *events)
	}

	waittest.ProcessGone(t, descendantPID)

	for _, e := range spy.Entries() {
		if e.Level == slog.LevelWarn && e.Msg == "agent stdout was not fully collected before the turn ended" {
			t.Errorf("unexpected stdout abandonment WARN record emitted; the group kill should have released the reader before the bound fired: %+v", e)
		}
	}
}

// TestForkPerTurnSession_EscapedDescendantHoldsStdout drives
// writeEscapedPgidScript's setsid descendant, which survives the group
// kill and keeps holding the standard-output handle open, through a
// cancelled RunTurn. It asserts that the turn publishes within
// sess.drainGrace, measured from the reap rather than from the turn's
// start, that the abandonment record fires exactly once, and, together
// with TestForkPerTurnSession_InGroupDescendantHoldsStdout, that it ends
// with the same disposition and error as the identical unabandoned turn.
func TestForkPerTurnSession_EscapedDescendantHoldsStdout(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "escaped.pid")
	script := writeEscapedPgidScript(t, tmpDir, pidFile)

	spy := &agenttest.LogSpy{}
	target := newTestTarget(tmpDir, script)
	sess := NewForkPerTurnSession(target, noopHooks(), slog.New(spy), 0)
	// Matches this file's own 200ms seam rather than forkperturn_test.go's
	// 50ms one: the kill only sends the signal before Reaper.Done closes,
	// and the descendant's death and its release of the handle follow
	// asynchronously, so a shorter grace would race the scheduler instead
	// of measuring the release.
	sess.drainGrace = 200 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emit, events := sinkEvents()
	type outcome struct {
		result domain.TurnResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := sess.RunTurn(ctx, "p", emit)
		done <- outcome{result, err}
	}()

	waittest.PIDFile(t, pidFile)
	t.Cleanup(func() { agenttest.KillEscaped(pidFile) })

	reapStart := time.Now()
	cancel()

	got := waittest.Receive(t, done, "RunTurn outcome")
	elapsed := time.Since(reapStart)

	if elapsed > procutil.DefaultDrainGrace {
		t.Errorf("RunTurn published %v after cancellation, want under the default drain grace %v: sess.drainGrace=200ms must bound it", elapsed, procutil.DefaultDrainGrace)
	}

	requireAgentError(t, got.err, domain.ErrTurnCancelled)
	if !hasEventType(*events, domain.EventTurnCancelled) {
		t.Errorf("EventTurnCancelled not emitted; got %v", *events)
	}

	var abandonCount int
	for _, e := range spy.Entries() {
		if e.Level == slog.LevelWarn && e.Msg == "agent stdout was not fully collected before the turn ended" {
			abandonCount++
		}
	}
	if abandonCount != 1 {
		t.Errorf("abandonment WARN record count = %d, want exactly 1", abandonCount)
	}
}
