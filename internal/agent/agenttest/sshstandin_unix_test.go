//go:build unix

package agenttest_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
)

func TestSSHStandIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{"remote command sees only PATH", []string{"-R", "127.0.0.1:0:127.0.0.1:18899", "--", "host", "printf '%s' \"${LEAK-unset}\""}, 0},
		{"allocated forward without a -R argument", []string{"host", "true"}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ssh := agenttest.NewSSHStandIn(t, t.TempDir(), agenttest.SSHStandIn{PATH: os.Getenv("PATH"), Forward: agenttest.SSHForwardAllocated})
			cmd := exec.CommandContext(t.Context(), ssh, tt.args...) //nolint:gosec // G204: the command is this test's own stand-in.
			cmd.Env = append(os.Environ(), "LEAK=inherited")
			var stdout bytes.Buffer
			cmd.Stdout = &stdout

			err := cmd.Run()

			code := 0
			if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatalf("Run(%q): %v", ssh, err)
			}
			if code != tt.wantCode {
				t.Errorf("ssh exit code = %d, want %d", code, tt.wantCode)
			}
			if tt.wantCode == 0 && stdout.String() != "unset" {
				t.Errorf("remote command saw LEAK = %q, want %q", stdout.String(), "unset")
			}
		})
	}
}
