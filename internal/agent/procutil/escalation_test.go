package procutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/waittest"
)

func init() {
	fakeScenarios["procutil.escalation-leader"] = agenttest.Typed(runEscalationLeader)
	fakeScenarios["procutil.escalation-descendant"] = agenttest.Typed(runEscalationDescendant)
}

type escalationLeaderParams struct {
	DescendantPath string
}

func runEscalationLeader(_ []string, p escalationLeaderParams) int {
	signal.Notify(make(chan os.Signal, 1), os.Interrupt, syscall.SIGTERM)

	cmd := exec.Command(p.DescendantPath) //nolint:gosec // fake runtime path under t.TempDir()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "escalation leader: start descendant: %v\n", err)
		return 1
	}
	_ = cmd.Wait() //nolint:errcheck // a stubborn descendant is only ever reaped by a force kill, whose error carries nothing this fixture needs
	return 0
}

type escalationDescendantParams struct {
	PIDFile      string
	Marker       string
	ExitOnSignal bool
}

func runEscalationDescendant(_ []string, p escalationDescendantParams) int {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)

	if err := os.WriteFile(p.PIDFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "escalation descendant: write pid: %v\n", err)
		return 1
	}

	if !p.ExitOnSignal {
		agenttest.Hang()
		return 0
	}

	<-ch
	if err := os.WriteFile(p.Marker, []byte("terminated"), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "escalation descendant: write marker: %v\n", err)
		return 1
	}
	return 0
}

const escalationGrace = 5 * time.Second

func TestArmGroupEscalation_ForceTerminatesStubbornGroupMember(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "descendant.pid")

	descendantPath := agenttest.FakeRuntime(t, dir, "descendant", "procutil.escalation-descendant", escalationDescendantParams{
		PIDFile: pidFile,
	})
	leaderPath := agenttest.FakeRuntime(t, dir, "leader", "procutil.escalation-leader", escalationLeaderParams{
		DescendantPath: descendantPath,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, leaderPath) //nolint:gosec // fake runtime path under t.TempDir()
	SetGroupCancel(cmd, escalationGrace)

	pipes, group, err := StartWithOwnedPipes(ctx, cmd, nil)
	if err != nil {
		t.Fatalf("StartWithOwnedPipes() = %v, want nil", err)
	}
	defer func() { _ = pipes.Close() }()

	t.Cleanup(func() { _ = group.Kill() })

	descendantPID := waittest.PIDFile(t, pidFile)

	cancel()
	_ = cmd.Wait() //nolint:errcheck // a cancelled command reports the cancellation, not a fault

	waittest.ProcessGone(t, descendantPID)
}

func TestArmGroupEscalation_GroupDrainedInsideGraceSendsNoForceSignal(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "descendant.terminated")
	pidFile := filepath.Join(dir, "descendant.pid")

	descendantPath := agenttest.FakeRuntime(t, dir, "descendant", "procutil.escalation-descendant", escalationDescendantParams{
		PIDFile:      pidFile,
		Marker:       marker,
		ExitOnSignal: true,
	})
	leaderPath := agenttest.FakeRuntime(t, dir, "leader", "procutil.escalation-leader", escalationLeaderParams{
		DescendantPath: descendantPath,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, leaderPath) //nolint:gosec // fake runtime path under t.TempDir()
	SetGroupCancel(cmd, escalationGrace)

	pipes, group, err := StartWithOwnedPipes(ctx, cmd, nil)
	if err != nil {
		t.Fatalf("StartWithOwnedPipes() = %v, want nil", err)
	}
	defer func() { _ = pipes.Close() }()

	t.Cleanup(func() { _ = group.Kill() })

	descendantPID := waittest.PIDFile(t, pidFile)

	forceSends, waitForQuiet := armEscalationForceSendProbe(t, cmd.Process.Pid)
	reaper := StartReaper(cmd, nil)

	cancelledAt := time.Now()
	cancel()
	waittest.Receive(t, reaper.Done(), "reaper Done()")
	sendsAtRelease := forceSends.Load()

	waittest.File(t, marker)
	waittest.ProcessGone(t, descendantPID)

	floor := cancelledAt.Add(escalationGrace + groupDrainBound)
	waitForQuiet(500*time.Millisecond, floor, escalationGrace+groupDrainBound+10*time.Second)
	if calls := forceSends.Load() - sendsAtRelease; calls != 0 {
		t.Errorf("force-termination seam called %d times after the reap released a group that drained inside the grace, want 0", calls)
	}
}
