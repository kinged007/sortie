//go:build linux

package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/agent/agenttest/launchtest"
	"github.com/sortie-ai/sortie/internal/agent/procutil"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/waittest"
)

// scenarioCodexSSHStandIn names the Go fake runtime registered below
// into fakeScenarios.
const scenarioCodexSSHStandIn = "codex.ssh-stand-in"

func init() {
	fakeScenarios[scenarioCodexSSHStandIn] = agenttest.Typed(runCodexSSHStandIn)
}

// sshStandInParams configures [runCodexSSHStandIn]: PATH is the PATH
// value its dropped-environment child receives.
type sshStandInParams struct {
	PATH string
}

// runCodexSSHStandIn is a stand-in "ssh" runtime: it ignores every
// argument ahead of the last one, the remote command
// sshutil.BuildSSHLaunch produced, and runs that command through sh -c
// with its own environment dropped and replaced by params.PATH alone,
// and its standard input, output, and error inherited. Dropping the
// environment is what proves a carried variable reaches the remote
// command only through the SSH session's standard input, never through
// this process's own inherited environment.
func runCodexSSHStandIn(args []string, params sshStandInParams) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "ssh stand-in: no arguments")
		return 2
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh stand-in: sh not found: %v\n", err)
		return 2
	}

	remoteCommand := args[len(args)-1]
	cmd := exec.Command(shPath, "-c", remoteCommand) //nolint:gosec // remoteCommand is the launch this test built
	cmd.Env = []string{"PATH=" + params.PATH}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "ssh stand-in: %v\n", err)
		return 2
	}
	return 0
}

// sshStandInEnvPath returns the PATH value the ssh stand-in's dropped-
// environment child should receive: a directory holding only a
// symlink to sh, and, when includeDD is true, a symlink to dd as well.
// Building an isolated directory rather than reusing sh's own
// directory matters because a real sh and a real dd usually share one
// directory (/usr/bin, /bin), so reusing it for the no-dd case would
// resolve dd anyway.
func sshStandInEnvPath(t *testing.T, includeDD bool) string {
	t.Helper()

	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not found on PATH: %v", err)
	}

	dir := t.TempDir()
	if err := os.Symlink(shPath, filepath.Join(dir, "sh")); err != nil {
		t.Fatalf("Symlink(sh): %v", err)
	}

	if includeDD {
		ddPath, err := exec.LookPath("dd")
		if err != nil {
			t.Skipf("dd not found on PATH: %v", err)
		}
		if err := os.Symlink(ddPath, filepath.Join(dir, "dd")); err != nil {
			t.Fatalf("Symlink(dd): %v", err)
		}
	}
	return dir
}

// writeCodexHandshakeScriptWithEnvCapture writes carriedName's value to
// capturePath before answering codex's three-call handshake exactly as
// writeFakeAppServerScript does, then idles on standard input until it
// closes. It idles on the read built-in rather than on sleep because
// the stand-in's PATH holds only sh and dd, so a sleep loop would spin
// on a command that cannot resolve.
func writeCodexHandshakeScriptWithEnvCapture(t *testing.T, dir, carriedName, capturePath string) string {
	t.Helper()
	content := "printf '%s' \"$" + carriedName + "\" > '" + capturePath + "'\n" +
		"read -r _init_req\n" +
		"printf '{\"id\":1,\"result\":{}}\\n'\n" +
		"read -r _initialized_notif\n" +
		"read -r _account_read_req\n" +
		"printf '{\"id\":2,\"result\":{}}\\n'\n" +
		"read -r _thread_start_req\n" +
		"printf '{\"id\":3,\"result\":{\"thread\":{\"id\":\"fake-thread-1\"}}}\\n'\n" +
		"printf '{\"method\":\"thread/started\",\"params\":{}}\\n'\n" +
		"while IFS= read -r _; do :; done\n"
	return agenttest.WriteScript(t, dir, "fake-codex-app-server-env", content)
}

// TestStartSession_SSH_CarriesEnvironmentVariable drives a remote
// session through the stand-in ssh runtime and asserts that the fake
// app-server observes a carried variable's value, delivered only
// through the SSH session's standard input rather than through the
// stand-in's own inherited environment, since the stand-in drops its
// environment before running the remote command.
//
// Not run with t.Parallel(): it pins CODEX_API_KEY via t.Setenv, and
// sets PATH and the carried variable the same way.
func TestStartSession_SSH_CarriesEnvironmentVariable(t *testing.T) {
	t.Setenv("CODEX_API_KEY", "")

	tmpDir := t.TempDir()
	sshDir := t.TempDir()

	agenttest.FakeRuntime(t, sshDir, "ssh", scenarioCodexSSHStandIn, sshStandInParams{PATH: sshStandInEnvPath(t, true)})
	t.Setenv("PATH", sshDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	const carriedName = "CODEX_TEST_CARRY"
	const carriedValue = "carried-value-codex"
	t.Setenv(carriedName, carriedValue)

	capturePath := filepath.Join(tmpDir, "captured.txt")
	agentScript := writeCodexHandshakeScriptWithEnvCapture(t, tmpDir, carriedName, capturePath)

	adapter, err := NewCodexAdapter()
	if err != nil {
		t.Fatalf("NewCodexAdapter() error = %v, want nil", err)
	}

	session, err := adapter.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath: tmpDir,
		AgentConfig:   domain.AgentConfig{Command: agentScript},
		SSHHost:       "user@stand-in-host",
		SSHEnvNames:   []string{carriedName},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := adapter.StopSession(context.Background(), session); err != nil {
			t.Errorf("StopSession() error = %v, want nil", err)
		}
	})

	got, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("ReadFile(captured.txt): %v", err)
	}
	if string(got) != carriedValue {
		t.Errorf("remote app-server observed %q, want %q", string(got), carriedValue)
	}
}

// TestStartSession_SSH_NoDDEndsAsPortExit asserts that a remote host
// without dd fails StartSession as port_exit, the shared early-exit
// report carrying the guard's own exit status and standard error, and
// never confuses it with a missing agent binary.
//
// Not run with t.Parallel(): it pins CODEX_API_KEY via t.Setenv, and
// sets PATH and the carried variable the same way.
func TestStartSession_SSH_NoDDEndsAsPortExit(t *testing.T) {
	t.Setenv("CODEX_API_KEY", "")

	tmpDir := t.TempDir()
	sshDir := t.TempDir()

	agenttest.FakeRuntime(t, sshDir, "ssh", scenarioCodexSSHStandIn, sshStandInParams{PATH: sshStandInEnvPath(t, false)})
	t.Setenv("PATH", sshDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	const carriedName = "CODEX_TEST_CARRY_NODD"
	t.Setenv(carriedName, "some-value")

	capturePath := filepath.Join(tmpDir, "captured.txt")
	agentScript := writeCodexHandshakeScriptWithEnvCapture(t, tmpDir, carriedName, capturePath)

	adapter, err := NewCodexAdapter()
	if err != nil {
		t.Fatalf("NewCodexAdapter() error = %v, want nil", err)
	}

	_, err = adapter.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath: tmpDir,
		AgentConfig:   domain.AgentConfig{Command: agentScript},
		SSHHost:       "user@stand-in-host",
		SSHEnvNames:   []string{carriedName},
	})

	const ddMissingMessage = "sortie: dd is required on the remote host to receive environment variables"

	var agentErr *domain.AgentError
	if !errors.As(err, &agentErr) {
		t.Fatalf("StartSession() error = %v (%T), want a non-nil *domain.AgentError", err, err)
	}
	if agentErr.Kind != domain.ErrPortExit {
		t.Errorf("StartSession() error kind = %q, want %q (never the agent-not-found category)", agentErr.Kind, domain.ErrPortExit)
	}
	var earlyExitErr *agentcore.EarlyExitError
	if !errors.As(agentErr.Err, &earlyExitErr) {
		t.Fatalf("StartSession() error = %v, chain does not hold an *agentcore.EarlyExitError", agentErr)
	}
	if earlyExitErr.Status() != "exit status 1" {
		t.Errorf("EarlyExitError.Status() = %q, want %q", earlyExitErr.Status(), "exit status 1")
	}
	if !strings.Contains(earlyExitErr.Output(), ddMissingMessage) {
		t.Errorf("EarlyExitError.Output() = %q, want it to contain %q", earlyExitErr.Output(), ddMissingMessage)
	}

	if _, statErr := os.Stat(capturePath); statErr == nil {
		t.Error("the fake app-server ran with no dd on PATH, want it never to run")
	}
}

// writeDescendantScript creates the script the fake app-server spawns as
// a background job. It publishes its own PID to pidFile, then idles until
// a catchable termination signal arrives and records that it caught one
// by writing markerFile.
//
// The marker is the evidence that a graceful signal reached the whole
// process group rather than only its leader: this script is a grandchild
// of the adapter, so it can only be signalled through the group.
func writeDescendantScript(t *testing.T, dir, pidFile, markerFile string) string {
	t.Helper()
	content := fmt.Sprintf(
		"MARKER='%s'\n"+
			"PID_FILE='%s'\n"+
			"trap 'printf terminated > \"$MARKER\"; exit 0' TERM\n"+
			"printf '%%s\\n' \"$$\" > \"$PID_FILE\"\n"+
			// wait returns as soon as a trapped signal arrives, while a
			// sleep loop runs the handler only after the current sleep
			// ends. That delay competes with the grace period before the
			// group is killed outright, which is the whole budget this
			// fixture has to write its marker in.
			"sleep 3600 &\n"+
			"wait\n",
		markerFile, pidFile,
	)
	return agenttest.WriteScript(t, dir, "fake-codex-descendant", content)
}

// writeFakeAppServerScript creates a script that fakes just enough of the
// codex app-server JSON-RPC handshake (initialize, account/read,
// thread/start) for StartSession to succeed, then spawns descendantScript
// as a background job and blocks until that descendant exits.
//
// The script waits for its descendant inside its own TERM handler rather
// than exiting immediately. That ordering is what makes the assertions
// deterministic: the adapter's exit watcher force-kills the group only
// after cmd.Wait returns, and cmd.Wait cannot return while this script is
// still waiting, so the force kill can never race the descendant's
// handler.
//
// Response ids are hardcoded (1, 2, 3) rather than parsed from the
// request lines: jsonrpc.Conn allocates ids sequentially starting at 1,
// and StartSession issues exactly these three calls in this order, so the
// sequence is deterministic. Using agenttest.WriteScript avoids the
// ETXTBSY race on Linux.
func writeFakeAppServerScript(t *testing.T, dir, descendantScript string) string {
	t.Helper()
	content := fmt.Sprintf(
		"read -r _init_req\n"+
			"printf '{\"id\":1,\"result\":{}}\\n'\n"+
			"read -r _initialized_notif\n"+
			"read -r _account_read_req\n"+
			"printf '{\"id\":2,\"result\":{}}\\n'\n"+
			"read -r _thread_start_req\n"+
			"printf '{\"id\":3,\"result\":{\"thread\":{\"id\":\"fake-thread-1\"}}}\\n'\n"+
			"printf '{\"method\":\"thread/started\",\"params\":{}}\\n'\n"+
			"'%s' &\n"+
			"DESCENDANT_PID=$!\n"+
			"trap 'wait \"$DESCENDANT_PID\"; exit 0' TERM\n"+
			"wait \"$DESCENDANT_PID\"\n",
		descendantScript,
	)
	return agenttest.WriteScript(t, dir, "fake-codex-app-server", content)
}

// TestStartSession_CancelSignalsProcessGroup verifies that cancelling the
// context passed to StartSession, after the session has been established,
// tears the app-server down through its process group: the group receives
// a catchable termination signal rather than os/exec's default force kill
// of the direct child alone, and no descendant survives.
//
// The load-bearing assertion is the descendant's marker. A descendant can
// only be reached through the group, and it can only write the marker if
// the signal was catchable, so the marker fails both when the signal
// never reaches the group and when it arrives as an uncatchable kill.
// Asserting only that the descendant is gone would not distinguish the
// two: the adapter's exit watcher force-kills the group unconditionally
// once cmd.Wait returns, and that alone reaps the descendant either way.
//
// Not run with t.Parallel(): it pins CODEX_API_KEY to empty via t.Setenv
// so the fake app-server's account/read reply always takes the no-login
// branch in authenticateIfNeeded, regardless of the ambient environment,
// and t.Setenv forbids parallel use.
func TestStartSession_CancelSignalsProcessGroup(t *testing.T) {
	t.Setenv("CODEX_API_KEY", "")

	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "descendant.pid")
	markerFile := filepath.Join(tmpDir, "descendant.terminated")
	descendant := writeDescendantScript(t, tmpDir, pidFile, markerFile)
	script := writeFakeAppServerScript(t, tmpDir, descendant)

	adapter, err := NewCodexAdapter()
	if err != nil {
		t.Fatalf("NewCodexAdapter() error = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session, err := adapter.StartSession(ctx, domain.StartSessionParams{
		WorkspacePath: tmpDir,
		AgentConfig:   domain.AgentConfig{Command: script},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v, want nil", err)
	}

	directPID, convErr := strconv.Atoi(session.AgentPID)
	if convErr != nil {
		t.Fatalf("session.AgentPID = %q, want a PID: %v", session.AgentPID, convErr)
	}
	launchtest.KillOnCleanup(t, session.AgentPID)

	descendantPID := waittest.PIDFile(t, pidFile)

	cancel()

	waittest.File(t, markerFile)
	waittest.ProcessGone(t, directPID)
	waittest.ProcessGone(t, descendantPID)
}

// writeFakeAppServerScriptEscapedTurn creates a script that fakes the
// codex app-server handshake exactly as writeFakeAppServerScript does,
// then answers one turn/start call (id 4, the next identifier
// jsonrpc.Conn allocates after the handshake's three calls) and starts a
// setsid grandchild that inherits standard output and exits without
// waiting for it, to drive a real turn whose runtime exits while an
// escaped descendant still holds the output handle.
func writeFakeAppServerScriptEscapedTurn(t *testing.T, dir, pidFile string) string {
	t.Helper()
	content := fmt.Sprintf(`read -r _init_req
printf '{"id":1,"result":{}}\n'
read -r _initialized_notif
read -r _account_read_req
printf '{"id":2,"result":{}}\n'
read -r _thread_start_req
printf '{"id":3,"result":{"thread":{"id":"fake-thread-1"}}}\n'
printf '{"method":"thread/started","params":{}}\n'
read -r _turn_start_req
printf '{"id":4,"result":{"turn":{"id":"t1"}}}\n'
setsid sh -c 'echo $$ > %s; sleep 3600' 2>/dev/null &
while [ ! -s %s ]; do sleep 0.01; done
exit 0
`, pidFile, pidFile)
	return agenttest.WriteScript(t, dir, "fake-codex-app-server-escaped-turn", content)
}

// TestStartSession_ReleaseEndsTurnWhenEscapedDescendantHoldsOutput
// asserts that a session whose runtime exits while an escaped
// descendant holds the output handle publishes the turn's outcome and
// completes StopSession within the injected CodexAdapter.drainGrace
// bound, leaving no goroutine of the session running.
//
// Not run with t.Parallel(): it pins CODEX_API_KEY via t.Setenv, which
// forbids parallel use.
func TestStartSession_ReleaseEndsTurnWhenEscapedDescendantHoldsOutput(t *testing.T) {
	t.Setenv("CODEX_API_KEY", "")
	agenttest.RequireSetsid(t)

	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "escaped.pid")
	t.Cleanup(func() { agenttest.KillEscaped(pidFile) })
	script := writeFakeAppServerScriptEscapedTurn(t, tmpDir, pidFile)

	adapter := &CodexAdapter{drainGrace: 300 * time.Millisecond}
	session, err := adapter.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath: tmpDir,
		AgentConfig:   domain.AgentConfig{Command: script},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v, want nil", err)
	}
	state, ok := session.Internal.(*sessionState)
	if !ok {
		t.Fatalf("session.Internal type = %T, want *sessionState", session.Internal)
	}

	start := time.Now()
	result, runErr := adapter.RunTurn(context.Background(), session, domain.RunTurnParams{
		Prompt:  "work",
		OnEvent: func(domain.AgentEvent) {},
	})
	elapsed := time.Since(start)

	if elapsed > procutil.DefaultDrainGrace {
		t.Errorf("RunTurn() took %v, want under the default drain grace %v (bounded by CodexAdapter.drainGrace)", elapsed, procutil.DefaultDrainGrace)
	}

	var agentErr *domain.AgentError
	if !errors.As(runErr, &agentErr) || agentErr.Kind != domain.ErrPortExit {
		t.Fatalf("RunTurn() error = %v, want AgentError{Kind: %q}", runErr, domain.ErrPortExit)
	}
	if result.ExitReason != domain.EventTurnFailed {
		t.Errorf("ExitReason = %q, want %q", result.ExitReason, domain.EventTurnFailed)
	}
	// This turn is past its turn/start response, so the connection's end
	// reaches it through the event loop rather than through the call.
	// Whichever of those two arms wins the race reports the abandonment,
	// so asserting the message here is what keeps either of them from
	// silently reverting to its transport text.
	if agentErr.Message != procutil.OutputAbandonedMessage {
		t.Errorf("AgentError.Message = %q, want %q", agentErr.Message, procutil.OutputAbandonedMessage)
	}

	stopStart := time.Now()
	if err := adapter.StopSession(context.Background(), session); err != nil {
		t.Errorf("StopSession() = %v, want nil", err)
	}
	if elapsed := time.Since(stopStart); elapsed > 2*time.Second {
		t.Errorf("StopSession() took %v, want well under 2s", elapsed)
	}

	select {
	case <-state.readerDone:
	default:
		t.Error("session leak: the connection's reader goroutine is still running after StopSession returned")
	}
}
