//go:build unix

package pi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/agent/agenttest/dispositiontest"
	"github.com/sortie-ai/sortie/internal/agent/procutil"
	"github.com/sortie-ai/sortie/internal/domain"
)

const resumeFixtureSessionID = "7a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"

// mustBuildSSHSession returns a session that launches script as the ssh
// binary, so the turn runs the remote-launch path against a local script.
func mustBuildSSHSession(t *testing.T, dir, script string) domain.Session {
	t.Helper()
	pt, faults := parsePassthroughConfig(map[string]any{})
	if len(faults) > 0 {
		t.Fatalf("parsePassthroughConfig() faults = %v", faults)
	}
	return domain.Session{Internal: &sessionState{
		target: agentcore.LaunchTarget{
			Command:       script,
			WorkspacePath: dir,
			RemoteCommand: "pi",
			SSHHost:       "example.test",
		},
		passthrough: pt,
		sessionID:   resumeFixtureSessionID,
		baseLogger:  slog.Default(),
		usage:       agentcore.NewTurnEndUsage(),
		drainGrace:  procutil.DefaultDrainGrace,
	}}
}

// writeCapturingFixtureScript returns a fake-pi that records its argument
// vector (one argument per line) and whole standard input, then replays
// the resume fixture.
func writeCapturingFixtureScript(t *testing.T, dir, argvPath, stdinPath string) string {
	t.Helper()
	fixture := filepath.Join(dir, "resume_turn.jsonl")
	data, err := os.ReadFile(filepath.Join("testdata", "resume_turn.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile(resume_turn.jsonl): %v", err)
	}
	if err := os.WriteFile(fixture, data, 0o644); err != nil {
		t.Fatalf("WriteFile(resume_turn.jsonl): %v", err)
	}
	return writePiScript(t, dir, `printf '%s\n' "$@" > '`+argvPath+`'
cat > '`+stdinPath+`'
sed "s|__CWD__|$PWD|g" '`+fixture+`'`)
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(data)
}

func TestStartSession_InvalidWorkspace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  domain.StartSessionParams
		wantErr domain.AgentErrorKind
	}{
		{
			name:    "empty_workspace_path",
			params:  domain.StartSessionParams{AgentConfig: domain.AgentConfig{Command: "/bin/sh"}},
			wantErr: domain.ErrInvalidWorkspaceCwd,
		},
		{
			name: "non_existent_workspace",
			params: domain.StartSessionParams{
				WorkspacePath: "/nonexistent/path/sortie-test-xyz",
				AgentConfig:   domain.AgentConfig{Command: "/bin/sh"},
			},
			wantErr: domain.ErrInvalidWorkspaceCwd,
		},
		{
			name: "command_not_found",
			params: domain.StartSessionParams{
				WorkspacePath: t.TempDir(),
				AgentConfig:   domain.AgentConfig{Command: "sortie-nonexistent-binary-pi-xyz"},
			},
			wantErr: domain.ErrAgentNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, _ := NewPiAdapter()
			_, err := a.StartSession(context.Background(), tt.params)
			agentErr, ok := errors.AsType[*domain.AgentError](err)
			if !ok {
				t.Fatalf("StartSession() error = %v (%T), want a *domain.AgentError", err, err)
			}
			if agentErr.Kind != tt.wantErr {
				t.Errorf("Kind = %q, want %q", agentErr.Kind, tt.wantErr)
			}
		})
	}
}

func TestRunTurn_WrongInternalType(t *testing.T) {
	t.Parallel()

	a, _ := NewPiAdapter()
	_, err := a.RunTurn(context.Background(), domain.Session{ID: "test", Internal: "not-a-session-state"}, domain.RunTurnParams{
		Prompt:  "work",
		OnEvent: func(domain.AgentEvent) {},
	})

	agentErr, ok := errors.AsType[*domain.AgentError](err)
	if !ok || agentErr.Kind != domain.ErrResponseError {
		t.Fatalf("RunTurn() error = %v, want an AgentError of kind %q", err, domain.ErrResponseError)
	}
}

func TestStopSession_WrongInternalType(t *testing.T) {
	t.Parallel()

	a, _ := NewPiAdapter()
	err := a.StopSession(context.Background(), domain.Session{ID: "test", Internal: "not-a-session-state"})

	if _, ok := errors.AsType[*domain.AgentError](err); !ok {
		t.Fatalf("StopSession() error = %v (%T), want a *domain.AgentError", err, err)
	}
}

func TestRunTurn_ClosedSession(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a, _ := NewPiAdapter()
	session := mustStartSession(t, a, dir, writePiScript(t, dir, "exit 0"))
	if err := a.StopSession(context.Background(), session); err != nil {
		t.Fatalf("StopSession() error = %v", err)
	}

	_, err := a.RunTurn(context.Background(), session, domain.RunTurnParams{Prompt: "work", OnEvent: func(domain.AgentEvent) {}})

	agentErr, ok := errors.AsType[*domain.AgentError](err)
	if !ok || agentErr.Kind != domain.ErrResponseError || agentErr.Message != "session already stopped" {
		t.Fatalf("RunTurn() error = %v, want %q of kind %q", err, "session already stopped", domain.ErrResponseError)
	}
}

func TestRunTurn_ConcurrentRunRejected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a, _ := NewPiAdapter()
	session := mustStartSession(t, a, dir, writePiScript(t, dir, "exit 0"))
	state := session.Internal.(*sessionState)
	state.mu.Lock()
	state.active = &turnRuntime{waitCh: make(chan waitResult)}
	state.mu.Unlock()

	_, err := a.RunTurn(context.Background(), session, domain.RunTurnParams{Prompt: "work", OnEvent: func(domain.AgentEvent) {}})

	agentErr, ok := errors.AsType[*domain.AgentError](err)
	if !ok || agentErr.Kind != domain.ErrResponseError || agentErr.Message != "session already has an active turn" {
		t.Fatalf("RunTurn() error = %v, want %q of kind %q", err, "session already has an active turn", domain.ErrResponseError)
	}
}

func TestRunTurn_LinkedWorkspaceRefusedBeforeStart(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	marker := filepath.Join(workspace, "marker")
	scriptDir := t.TempDir()
	script := writePiScript(t, scriptDir, "touch '"+marker+"'")
	a, _ := NewPiAdapter()
	session := mustStartSession(t, a, workspace, script)

	if err := os.RemoveAll(workspace); err != nil {
		t.Fatalf("RemoveAll(workspace): %v", err)
	}
	if err := os.Symlink(t.TempDir(), workspace); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := a.RunTurn(context.Background(), session, domain.RunTurnParams{Prompt: "work", OnEvent: func(domain.AgentEvent) {}})

	agentErr, ok := errors.AsType[*domain.AgentError](err)
	if !ok || agentErr.Kind != domain.ErrInvalidWorkspaceCwd {
		t.Fatalf("RunTurn(linked workspace) error = %v, want an AgentError of kind %q", err, domain.ErrInvalidWorkspaceCwd)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("marker file exists, want the subprocess never to start")
	}
}

func TestRunTurn_OversizedStdoutLine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := writePiScript(t, dir, `head -c $((10*1024*1024+1)) /dev/zero | tr '\000' 'a'
printf '\n'`)
	a, _ := NewPiAdapter()
	session := mustStartSession(t, a, dir, script)

	events, result, err := collectEvents(t, a, session, "work")

	agentErr, ok := errors.AsType[*domain.AgentError](err)
	if !ok || agentErr.Kind != domain.ErrResponseError {
		t.Fatalf("RunTurn() error = %v, want an AgentError of kind %q", err, domain.ErrResponseError)
	}
	if result.ExitReason != domain.EventTurnFailed {
		t.Errorf("ExitReason = %q, want %q", result.ExitReason, domain.EventTurnFailed)
	}
	failed := eventsOfType(events, domain.EventTurnFailed)
	if len(failed) != 1 || failed[0].Message != "stdout read error" {
		t.Fatalf("turn_failed events = %+v, want exactly one with Message %q", failed, "stdout read error")
	}
	dispositiontest.AssertDispositionContract(t, agentcore.TurnEvidence{
		Terminal:          agentcore.TerminalFailure,
		TerminalErrorKind: domain.ErrResponseError,
		TerminalMessage:   "stdout read error",
	}, result, err)
}

func TestRunTurn_EventAgentPID(t *testing.T) {
	t.Parallel()

	t.Run("local_events_include_pid", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		a, _ := NewPiAdapter()
		session := mustStartSession(t, a, dir, writeRunFixtureScript(t, dir, "simple_turn.jsonl"))

		events, result, err := collectEvents(t, a, session, "work")
		if err != nil || result.ExitReason != domain.EventTurnCompleted {
			t.Fatalf("RunTurn() = %+v, %v, want a completed turn", result, err)
		}
		if len(events) == 0 || events[0].AgentPID == "" {
			t.Fatalf("events = %+v, want the first event to carry the subprocess pid", events)
		}
		for _, event := range events {
			if event.AgentPID != events[0].AgentPID {
				t.Errorf("event %q AgentPID = %q, want %q", event.Type, event.AgentPID, events[0].AgentPID)
			}
		}
	})

	t.Run("ssh_events_leave_pid_empty", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		script := writeCapturingFixtureScript(t, dir, filepath.Join(dir, "argv"), filepath.Join(dir, "stdin"))
		a, _ := NewPiAdapter()

		events, result, err := collectEvents(t, a, mustBuildSSHSession(t, dir, script), "work")
		if err != nil || result.ExitReason != domain.EventTurnCompleted {
			t.Fatalf("RunTurn() = %+v, %v, want a completed turn", result, err)
		}
		if len(events) == 0 {
			t.Fatal("events = 0, want > 0")
		}
		for _, event := range events {
			if event.AgentPID != "" {
				t.Errorf("event %q AgentPID = %q, want empty in ssh mode", event.Type, event.AgentPID)
			}
		}
	})
}

func TestRunTurn_ReadTimeoutBeforeFirstJSONEvent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a, _ := NewPiAdapter()
	session := mustStartSession(t, a, dir, writePiScript(t, dir, "sleep 5"))
	session.Internal.(*sessionState).agentConfig.ReadTimeoutMS = 100

	events, result, err := collectEvents(t, a, session, "work")

	if result.ExitReason != domain.EventTurnFailed {
		t.Errorf("ExitReason = %q, want %q", result.ExitReason, domain.EventTurnFailed)
	}
	agentErr, ok := errors.AsType[*domain.AgentError](err)
	if !ok || agentErr.Kind != domain.ErrResponseTimeout {
		t.Fatalf("RunTurn() error = %v, want an AgentError of kind %q", err, domain.ErrResponseTimeout)
	}
	const wantMessage = "timed out waiting for first pi json event"
	if agentErr.Message != wantMessage {
		t.Errorf("Message = %q, want %q", agentErr.Message, wantMessage)
	}
	if failed := eventsOfType(events, domain.EventTurnFailed); len(failed) != 1 {
		t.Fatalf("turn_failed event count = %d, want 1", len(failed))
	}
	dispositiontest.AssertDispositionContract(t, agentcore.TurnEvidence{
		Terminal:          agentcore.TerminalFailure,
		TerminalErrorKind: domain.ErrResponseTimeout,
		TerminalMessage:   wantMessage,
	}, result, err)
}

func abandonmentWarnCount(spy *agenttest.LogSpy) int {
	var n int
	for _, e := range spy.Entries() {
		if e.Level == slog.LevelWarn && e.Msg == "agent stdout was not fully collected before the turn ended" {
			n++
		}
	}
	return n
}

func assertProcessDead(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
			if i := strings.LastIndex(string(data), ")"); i >= 0 && i+2 < len(data) && data[i+2] == 'Z' {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("process %d still alive after %v, want gone", pid, timeout)
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(mustReadFile(t, path)))
	if err != nil || pid <= 0 {
		t.Fatalf("readPIDFile(%q) is not a valid PID: %v", path, err)
	}
	return pid
}

// TestRunTurn_DescendantHoldsStdout drives a descendant that keeps the
// standard-output handle open through the normal exit path. An in-group
// descendant is killed with the group, which releases the reader before
// the drain bound fires and so records no abandonment; an escaped one
// survives, the turn still ends within the bound, and exactly one
// abandonment is recorded. Both end with the same disposition.
func TestRunTurn_DescendantHoldsStdout(t *testing.T) {
	t.Parallel()

	const header = `printf '{"type":"session","version":3,"id":"holder-1","timestamp":"t","cwd":"%s"}\n' "$PWD"` + "\n"

	t.Run("in-group descendant: no abandonment, descendant dies", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		pidFile := filepath.Join(dir, "descendant.pid")
		script := writePiScript(t, dir, "sleep 3600 &\nprintf '%s\\n' \"$!\" > '"+pidFile+"'\n"+header+"exit 0\n")

		spy := &agenttest.LogSpy{}
		a, _ := NewPiAdapter()
		session := mustStartSession(t, a, dir, script)
		state := session.Internal.(*sessionState)
		state.baseLogger = slog.New(spy)
		state.drainGrace = 200 * time.Millisecond

		start := time.Now()
		_, result, err := collectEvents(t, a, session, "work")

		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("RunTurn() took %v, want well under 3s", elapsed)
		}
		if result.ExitReason != domain.EventTurnFailed {
			t.Errorf("ExitReason = %q, want %q", result.ExitReason, domain.EventTurnFailed)
		}
		if agentErr, ok := errors.AsType[*domain.AgentError](err); !ok || agentErr.Kind != domain.ErrTurnFailed {
			t.Errorf("RunTurn() error = %v, want an AgentError of kind %q", err, domain.ErrTurnFailed)
		}
		assertProcessDead(t, readPIDFile(t, pidFile), 3*time.Second)
		if got := abandonmentWarnCount(spy); got != 0 {
			t.Errorf("abandonment WARN count = %d, want 0", got)
		}
	})

	t.Run("escaped descendant: exactly one abandonment, turn still bounded", func(t *testing.T) {
		agenttest.RequireSetsid(t)
		t.Parallel()

		dir := t.TempDir()
		pidFile := filepath.Join(dir, "escaped.pid")
		killDescendantOnCleanup(t, pidFile)
		script := writePiScript(t, dir, fmt.Sprintf(
			"setsid sh -c 'echo $$ > %[1]s; sleep 3600' 2>/dev/null &\nwhile [ ! -s %[1]s ]; do sleep 0.01; done\n%[2]sexit 0\n", pidFile, header))

		spy := &agenttest.LogSpy{}
		a, _ := NewPiAdapter()
		session := mustStartSession(t, a, dir, script)
		state := session.Internal.(*sessionState)
		state.baseLogger = slog.New(spy)
		state.drainGrace = 200 * time.Millisecond

		start := time.Now()
		_, result, err := collectEvents(t, a, session, "work")

		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("RunTurn() took %v, want well under 3s", elapsed)
		}
		if got := abandonmentWarnCount(spy); got != 1 {
			t.Errorf("abandonment WARN count = %d, want exactly 1", got)
		}
		if result.ExitReason != domain.EventTurnFailed {
			t.Errorf("ExitReason = %q, want %q", result.ExitReason, domain.EventTurnFailed)
		}
		if agentErr, ok := errors.AsType[*domain.AgentError](err); !ok || agentErr.Kind != domain.ErrTurnFailed {
			t.Errorf("RunTurn() error = %v, want an AgentError of kind %q", err, domain.ErrTurnFailed)
		}
	})
}

func awaitReapedFromFile(pidFile string) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && convErr == nil && pid > 0 {
			if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
				return true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// TestRunTurn_ZeroExitCompletesWhenTheDeadlineFallsAfterTheReap cancels
// the turn's context after the runtime has exited zero but while a
// setsid-detached descendant still holds stderr open, so the turn is not
// yet published. A runtime that finished on its own is a completed turn,
// not a cancelled one.
func TestRunTurn_ZeroExitCompletesWhenTheDeadlineFallsAfterTheReap(t *testing.T) {
	agenttest.RequireSetsid(t)
	t.Parallel()

	dir := t.TempDir()
	holderPID := filepath.Join(dir, "stderr-holder.pid")
	runtimePID := filepath.Join(dir, "runtime.pid")
	killDescendantOnCleanup(t, holderPID)
	msg := assistantMessage("done", 5, 2)
	script := writePiScript(t, dir, fmt.Sprintf(`printf '%%s\n' "$$" > '%[1]s'
setsid sh -c 'echo $$ > %[2]s; sleep 3600' >/dev/null &
while [ ! -s %[2]s ]; do sleep 0.01; done
printf '{"type":"session","version":3,"id":"late-cancel","timestamp":"t","cwd":"%%s"}\n' "$PWD"
printf '%%s\n' '{"type":"message_end","message":%[3]s}'
printf '%%s\n' '{"type":"turn_end","message":%[3]s,"toolResults":[]}'
exit 0
`, runtimePID, holderPID, msg))

	a, _ := NewPiAdapter()
	session := mustStartSession(t, a, dir, script)
	session.Internal.(*sessionState).drainGrace = 200 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var once sync.Once
	var reaped bool
	onEvent := func(domain.AgentEvent) {
		once.Do(func() {
			reaped = awaitReapedFromFile(runtimePID)
			cancel()
		})
	}

	done := make(chan struct{})
	var result domain.TurnResult
	var runErr error
	go func() {
		defer close(done)
		result, runErr = a.RunTurn(ctx, session, domain.RunTurnParams{Prompt: "work", OnEvent: onEvent})
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunTurn did not return within 10s")
	}

	if !reaped {
		t.Fatal("the runtime was not reaped before the context was cancelled, want the cancellation to fall after the reap")
	}
	if runErr != nil {
		t.Errorf("RunTurn() error = %v, want nil for a runtime that exited zero before the deadline", runErr)
	}
	if result.ExitReason != domain.EventTurnCompleted {
		t.Errorf("ExitReason = %q, want %q", result.ExitReason, domain.EventTurnCompleted)
	}
}

func TestRunTurn_PromptTravelsOnStandardInput(t *testing.T) {
	t.Parallel()

	const prompt = "@/etc/hostname\n--help\n'quoted' $(not run)"

	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv.txt")
	stdinPath := filepath.Join(dir, "stdin.txt")
	script := writeCapturingFixtureScript(t, dir, argvPath, stdinPath)
	a, _ := NewPiAdapter()
	session := mustStartSession(t, a, dir, script)
	session.Internal.(*sessionState).sessionID = resumeFixtureSessionID

	_, result, err := collectEvents(t, a, session, prompt)
	if err != nil || result.ExitReason != domain.EventTurnCompleted {
		t.Fatalf("RunTurn() = %+v, %v, want a completed turn", result, err)
	}

	if got := mustReadFile(t, stdinPath); got != prompt {
		t.Errorf("subprocess standard input = %q, want the prompt alone %q", got, prompt)
	}
	argv := mustReadFile(t, argvPath)
	if strings.Contains(argv, "hostname") || strings.Contains(argv, "not run") {
		t.Errorf("argv = %q, want no part of the prompt on the command line", argv)
	}
	if strings.Contains(argv, "\n--\n") {
		t.Errorf("argv = %q, want no argument separator", argv)
	}
}

// TestRunTurn_LargePromptReachesTheRuntime asserts a prompt above the
// kernel's single-argument limit (128 KiB) is delivered whole.
func TestRunTurn_LargePromptReachesTheRuntime(t *testing.T) {
	t.Parallel()

	prompt := strings.Repeat("0123456789abcdef", 16*1024) + " end"

	dir := t.TempDir()
	stdinPath := filepath.Join(dir, "stdin.txt")
	script := writeCapturingFixtureScript(t, dir, filepath.Join(dir, "argv.txt"), stdinPath)
	a, _ := NewPiAdapter()
	session := mustStartSession(t, a, dir, script)
	session.Internal.(*sessionState).sessionID = resumeFixtureSessionID

	if _, _, err := collectEvents(t, a, session, prompt); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if got := mustReadFile(t, stdinPath); got != prompt {
		t.Errorf("standard input length = %d, want the %d-byte prompt whole", len(got), len(prompt))
	}
}

// TestRunTurn_LocalLaunchIgnoresSSHEnvNames asserts a local turn sends the
// local argument vector and a standard input holding the prompt alone
// whether or not LaunchTarget.SSHEnvNames names a set variable.
func TestRunTurn_LocalLaunchIgnoresSSHEnvNames(t *testing.T) {
	// Not parallel: sets the carried variable via t.Setenv.
	const varName = "SORTIE_PI_RUNTURN_LOCAL_INVARIANCE"
	t.Setenv(varName, "should-never-reach-a-local-launch")

	for _, tc := range []struct {
		name        string
		sshEnvNames []string
	}{
		{"SSHEnvNames absent", nil},
		{"SSHEnvNames naming a set variable", []string{varName}},
	} {
		dir := t.TempDir()
		argvPath := filepath.Join(dir, "argv.txt")
		stdinPath := filepath.Join(dir, "stdin.txt")
		script := writeCapturingFixtureScript(t, dir, argvPath, stdinPath)
		a, _ := NewPiAdapter()
		session := mustStartSession(t, a, dir, script)
		state := session.Internal.(*sessionState)
		state.sessionID = resumeFixtureSessionID
		state.target.SSHEnvNames = tc.sshEnvNames

		if _, _, err := collectEvents(t, a, session, "work"); err != nil {
			t.Fatalf("%s: RunTurn() error = %v", tc.name, err)
		}
		if strings.Contains(mustReadFile(t, argvPath), "StrictHostKeyChecking") {
			t.Errorf("%s: argv is an SSH option vector, want the local argument vector", tc.name)
		}
		if got := mustReadFile(t, stdinPath); got != "work" {
			t.Errorf("%s: standard input = %q, want the prompt alone on a local launch", tc.name, got)
		}
	}
}

// TestRemoteLaunch_ForwardsSessionAndWorkspace drives a real remote turn
// against a local stand-in for ssh and asserts the remote command line
// keeps --session, the project-trust flag and the workspace, leaves the
// prompt off the command line, and carries the target's host-key setting.
func TestRemoteLaunch_ForwardsSessionAndWorkspace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv.txt")
	stdinPath := filepath.Join(dir, "stdin.txt")
	script := writeCapturingFixtureScript(t, dir, argvPath, stdinPath)
	a, _ := NewPiAdapter()
	session := mustBuildSSHSession(t, dir, script)
	state := session.Internal.(*sessionState)
	state.passthrough.ProjectTrust = trustApprove
	state.target.SSHStrictHostKeyChecking = "yes"

	_, result, err := collectEvents(t, a, session, "work")
	if err != nil || result.ExitReason != domain.EventTurnCompleted {
		t.Fatalf("RunTurn() = %+v, %v, want a completed turn", result, err)
	}

	argv := mustReadFile(t, argvPath)
	for _, want := range []string{"StrictHostKeyChecking=yes", "example.test", "cd -- '" + dir + "'", "'--session' '" + resumeFixtureSessionID + "'", "'--approve'"} {
		if !strings.Contains(argv, want) {
			t.Errorf("ssh argv = %q, want it to carry %q", argv, want)
		}
	}
	if strings.Contains(argv, "'work'") {
		t.Errorf("ssh argv = %q, want the prompt off the remote command line", argv)
	}
	if got := mustReadFile(t, stdinPath); got != "work" {
		t.Errorf("standard input = %q, want the prompt alone when no variable is carried", got)
	}
}

// TestRemoteLaunch_CarriesSSHEnvAndWithholdsEnv asserts a remote turn
// forwards each set LaunchTarget.SSHEnvNames variable through the stdin
// preamble ahead of the prompt, keeps its value off the command line,
// and unsets each WithheldEnv name on the remote host.
func TestRemoteLaunch_CarriesSSHEnvAndWithholdsEnv(t *testing.T) {
	// Not parallel: sets the carried variable via t.Setenv.
	const (
		carried  = "SORTIE_PI_TEST_CARRIED_CREDENTIAL"
		withheld = "SORTIE_PI_TEST_WITHHELD_CREDENTIAL"
		value    = "carried-value-9f3"
	)
	t.Setenv(carried, value)
	t.Setenv(withheld, "withheld-value")

	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv.txt")
	stdinPath := filepath.Join(dir, "stdin.txt")
	script := writeCapturingFixtureScript(t, dir, argvPath, stdinPath)
	a, _ := NewPiAdapter()
	session := mustBuildSSHSession(t, dir, script)
	state := session.Internal.(*sessionState)
	state.target.SSHEnvNames = []string{carried, withheld}
	state.target.WithheldEnv = []string{withheld}

	_, result, err := collectEvents(t, a, session, "work")
	if err != nil || result.ExitReason != domain.EventTurnCompleted {
		t.Fatalf("RunTurn() = %+v, %v, want a completed turn", result, err)
	}

	stdin := mustReadFile(t, stdinPath)
	if want := "export " + carried + "='" + value + "'"; !strings.Contains(stdin, want) {
		t.Errorf("standard input = %q, want the preamble to carry %q", stdin, want)
	}
	if !strings.HasSuffix(stdin, "work") {
		t.Errorf("standard input = %q, want the prompt after the preamble", stdin)
	}
	if strings.Contains(stdin, "withheld-value") || strings.Contains(stdin, withheld+"=") {
		t.Errorf("standard input = %q, want the withheld variable kept out of the preamble", stdin)
	}

	argv := mustReadFile(t, argvPath)
	if want := "unset " + withheld; !strings.Contains(argv, want) {
		t.Errorf("ssh argv = %q, want it to carry %q", argv, want)
	}
	if strings.Contains(argv, value) {
		t.Errorf("ssh argv = %q, want the carried value kept off the command line", argv)
	}
}

func TestRunTurn_CredentialVerificationIsEphemeral(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv.txt")
	script := writeCapturingFixtureScript(t, dir, argvPath, filepath.Join(dir, "stdin.txt"))
	a, _ := NewPiAdapter()
	session, err := a.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath:          dir,
		AgentConfig:            domain.AgentConfig{Command: script},
		CredentialVerification: true,
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	session.Internal.(*sessionState).sessionID = ""

	// The fixture header names a session id, so the turn may end in a
	// mismatch; only the launch is under test.
	_, _, _ = collectEvents(t, a, session, agentcore.CredentialVerificationPrompt)

	argv := strings.Split(strings.TrimRight(mustReadFile(t, argvPath), "\n"), "\n")
	if !hasConsecutive(argv, "--mode", "json") {
		t.Fatalf("argv = %v, want a pi json-mode launch", argv)
	}
	assertHasFlag(t, argv, "--no-session")
	assertNoFlag(t, argv, "--session")
}

// TestRunTurn_ToolDurationAndErrorMessage asserts a tool result carries
// the duration measured from its start event, pi's own figure when it
// reports one, and the tool's error text when it failed.
func TestRunTurn_ToolDurationAndErrorMessage(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	msg := assistantMessage("done", 5, 2)
	script := writePiScript(t, dir, `printf '{"type":"session","version":3,"id":"tools-1","timestamp":"t","cwd":"%s"}\n' "$PWD"
printf '%s\n' '{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{}}'
sleep 0.2
printf '%s\n' '{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":{"content":[{"type":"text","text":"exit status 1"}]},"isError":true}'
printf '%s\n' '{"type":"tool_execution_start","toolCallId":"c2","toolName":"read","args":{}}'
printf '%s\n' '{"type":"tool_execution_end","toolCallId":"c2","toolName":"read","result":{"content":[{"type":"text","text":"file body"}]},"isError":false,"durationMs":1234}'
printf '%s\n' '{"type":"tool_execution_end","toolCallId":"never-started","toolName":"ls","result":{"content":[]},"isError":false}'
printf '%s\n' '{"type":"message_end","message":`+msg+`}'
`)
	a, _ := NewPiAdapter()
	session := mustStartSession(t, a, dir, script)

	events, _, err := collectEvents(t, a, session, "work")
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}

	tools := eventsOfType(events, domain.EventToolResult)
	if len(tools) != 3 {
		t.Fatalf("tool results = %+v, want 3", tools)
	}
	if tools[0].ToolDurationMS < 150 || tools[0].ToolDurationMS > 5000 {
		t.Errorf("first tool ToolDurationMS = %d, want the ~200 ms between its start and end", tools[0].ToolDurationMS)
	}
	if !tools[0].ToolError || tools[0].Message != "exit status 1" {
		t.Errorf("first tool = %+v, want ToolError with Message %q", tools[0], "exit status 1")
	}
	if tools[1].ToolDurationMS != 1234 {
		t.Errorf("second tool ToolDurationMS = %d, want pi's own figure 1234", tools[1].ToolDurationMS)
	}
	if tools[1].ToolError || tools[1].Message != "" {
		t.Errorf("second tool = %+v, want no error and no message for a successful result", tools[1])
	}
	if tools[2].ToolDurationMS != 0 {
		t.Errorf("third tool ToolDurationMS = %d, want 0 for a tool whose start was never seen", tools[2].ToolDurationMS)
	}
}
