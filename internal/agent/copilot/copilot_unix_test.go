//go:build unix

package copilot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/agent/procutil"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/waittest"
)

const versionHeldDescendantScenario = "copilot.version-held-descendant"

type versionHeldDescendantParams struct {
	ChildPath    string
	ChildPIDPath string
}

func runVersionHeldDescendant(args []string, p versionHeldDescendantParams) int {
	if len(args) == 0 || args[0] != "--version" {
		return 0
	}
	if err := startHeldDescendant(p.ChildPath, p.ChildPIDPath); err != nil {
		fmt.Fprintf(os.Stderr, "version held descendant: %v\n", err)
		return 2
	}
	fmt.Println("copilot version 1.2.3")
	return 0
}

func startHeldDescendant(childPath, childPIDPath string) error {
	cmd := exec.Command(childPath) //nolint:gosec // fake runtime path this scenario was handed
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	return os.WriteFile(childPIDPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
}

func init() {
	fakeScenarios[versionHeldDescendantScenario] = agenttest.Typed(runVersionHeldDescendant)
}

func TestStartSession_VersionCanaryHeldDescendantHoldingOutput(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token-for-unit-test")

	dir := t.TempDir()
	descendantPath := agenttest.FakeRuntime(t, dir, "descendant", agenttest.OutputScenario, agenttest.Output{Hang: true})
	pidPath := filepath.Join(dir, "child.pid")
	binPath := agenttest.FakeRuntime(t, dir, "copilot", versionHeldDescendantScenario, versionHeldDescendantParams{
		ChildPath:    descendantPath,
		ChildPIDPath: pidPath,
	})

	adapter, err := NewCopilotAdapter()
	if err != nil {
		t.Fatalf("NewCopilotAdapter() error = %v", err)
	}

	start := time.Now()
	_, err = adapter.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath: t.TempDir(),
		AgentConfig:   domain.AgentConfig{Command: binPath},
	})
	if elapsed := time.Since(start); elapsed > procutil.DefaultDrainGrace {
		t.Errorf("StartSession() took %v, want under the default drain grace %v", elapsed, procutil.DefaultDrainGrace)
	}
	if err != nil {
		t.Fatalf("StartSession() error = %v, want nil", err)
	}

	waittest.ProcessGone(t, waittest.PIDFile(t, pidPath))
}
