//go:build linux

package procutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/waittest"
)

func init() {
	fakeScenarios["procutil.reaper-leader"] = agenttest.Typed(runReaperLeader)
}

type reaperLeaderParams struct {
	ChildPath string
	PIDFile   string
}

func runReaperLeader(_ []string, params reaperLeaderParams) int {
	cmd := exec.Command(params.ChildPath) //nolint:gosec // fake runtime path under t.TempDir()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "reaper leader: start child: %v\n", err)
		return 1
	}
	if err := os.WriteFile(params.PIDFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "reaper leader: write pid: %v\n", err)
		return 1
	}
	return 0
}

func TestReaper_KillsProcessGroupBeforeDoneCloses(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	childPath := agenttest.FakeRuntime(t, dir, "reaper-child", agenttest.OutputScenario, agenttest.Output{Hang: true})
	leaderPath := agenttest.FakeRuntime(t, dir, "reaper-leader", "procutil.reaper-leader", reaperLeaderParams{
		ChildPath: childPath,
		PIDFile:   pidFile,
	})

	cmd := exec.Command(leaderPath) //nolint:gosec // fake runtime path under t.TempDir()
	startOwned(t, cmd)

	childPID := waittest.PIDFile(t, pidFile)

	r := StartReaper(cmd, nil)
	waittest.Receive(t, r.Done(), "reaper Done()")

	waittest.ProcessGone(t, childPID)
}
