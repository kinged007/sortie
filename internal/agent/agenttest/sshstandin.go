package agenttest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// SSHStandInScenario names the built-in [Scenario] that runs [SSHStandIn.Run].
const SSHStandInScenario = "agenttest.sshstandin"

// SSHForward selects what the stand-in reports on standard error for the -R
// forward.
type SSHForward string

const (
	// SSHForwardSilent reports nothing, as ssh does when its log level hides
	// the allocation line.
	SSHForwardSilent SSHForward = ""
	// SSHForwardAllocated reports a remote port equal to the host port.
	SSHForwardAllocated SSHForward = "allocated"
	// SSHForwardRefused reports that the server refused the forward.
	SSHForwardRefused SSHForward = "refused"
)

// SSHStandIn parameterizes [SSHStandInScenario].
type SSHStandIn struct {
	// PATH is the only environment variable the remote command receives.
	PATH string
	// Forward selects the forward report. Any value but [SSHForwardSilent]
	// requires a well-formed -R argument, or the stand-in exits 2 rather than
	// hide a miswired launch.
	Forward SSHForward
}

// NewSSHStandIn creates under dir a [FakeRuntime] named ssh and returns its
// path. Put dir ahead of PATH for the launch under test. The calling package's
// TestMain must call [Main]. The stand-in spawns sh, so it needs a unix host.
func NewSSHStandIn(t testing.TB, dir string, params SSHStandIn) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the ssh stand-in spawns sh")
	}
	return FakeRuntime(t, dir, "ssh", SSHStandInScenario, params)
}

// Run runs the last argument, the remote command, through sh -c with PATH as
// its only environment, so a carried variable can reach it only through the
// session's standard input. It first writes the [SSHStandIn.Forward] report to
// standard error.
func (p SSHStandIn) Run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "ssh stand-in: no arguments")
		return 2
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh stand-in: sh not found: %v\n", err)
		return 2
	}

	report, err := p.forwardReport(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh stand-in: %v\n", err)
		return 2
	}
	if report != "" {
		fmt.Fprintln(os.Stderr, report)
	}

	cmd := exec.CommandContext(context.Background(), shPath, "-c", args[len(args)-1]) //nolint:gosec // G204: the remote command is the launch the test built.
	cmd.Env = []string{"PATH=" + p.PATH}
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

func runSSHStandIn(args []string, params SSHStandIn) int {
	return params.Run(args)
}

func (p SSHStandIn) forwardReport(args []string) (string, error) {
	switch p.Forward {
	case SSHForwardSilent:
		return "", nil
	case SSHForwardAllocated, SSHForwardRefused:
	default:
		return "", fmt.Errorf("unknown forward outcome %q", p.Forward)
	}

	hostPort, err := forwardHostPort(args)
	if err != nil {
		return "", err
	}
	if p.Forward == SSHForwardRefused {
		return "Warning: remote port forwarding failed for listen port 0", nil
	}
	return fmt.Sprintf("Allocated port %d for remote forward to 127.0.0.1:%d", hostPort, hostPort), nil
}

func forwardHostPort(args []string) (int, error) {
	i := slices.Index(args, "-R")
	if i < 0 || i+1 >= len(args)-1 {
		return 0, errors.New("no -R forward in arguments")
	}
	parts := strings.Split(args[i+1], ":")
	if len(parts) != 4 || parts[0] != "127.0.0.1" || parts[1] != "0" || parts[2] != "127.0.0.1" {
		return 0, fmt.Errorf("-R %q, want 127.0.0.1:0:127.0.0.1:<port>", args[i+1])
	}
	port, err := strconv.Atoi(parts[3])
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("-R %q carries no valid host port", args[i+1])
	}
	return port, nil
}
