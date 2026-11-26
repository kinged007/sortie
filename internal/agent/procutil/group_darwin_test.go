//go:build darwin

package procutil

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/waittest"
)

// The exit is observed through the test's own kqueue, registered before the
// kill so the notification cannot be missed, and leaves the child unreaped.
func awaitExitUnreaped(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	kq, err := unix.Kqueue()
	if err != nil {
		t.Fatalf("unix.Kqueue() = %v", err)
	}

	register := []unix.Kevent_t{{
		Ident:  uint64(cmd.Process.Pid), //nolint:gosec // G115: a process identifier is never negative
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}}
	if _, err := unix.Kevent(kq, register, nil, nil); err != nil {
		_ = unix.Close(kq)
		t.Fatalf("registering for the exit of process %d = %v", cmd.Process.Pid, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		_ = unix.Close(kq)
		t.Fatalf("cmd.Process.Kill() = %v", err)
	}

	notified := make(chan error, 1)
	go func() {
		defer func() { _ = unix.Close(kq) }()
		events := make([]unix.Kevent_t, 1)
		for {
			n, err := unix.Kevent(kq, nil, events, nil)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || n != 1 {
				notified <- fmt.Errorf("%d events, %v, want 1", n, err)
				return
			}
			notified <- nil
			return
		}
	}()
	if err := waittest.Receive(t, notified, "exit notification of the direct child"); err != nil {
		t.Fatalf("waiting for the exit of process %d = %v", cmd.Process.Pid, err)
	}
}

func awaitObservation(t *testing.T, done <-chan error, pid int) {
	t.Helper()
	if err := waittest.Receive(t, done, "observeLeaderExit() return"); err != nil {
		t.Errorf("observeLeaderExit(%d) = %v, want nil", pid, err)
	}
}

func TestObserveLeaderExitDoesNotReturnWhileRunning(t *testing.T) {
	t.Parallel()

	cmd := startLeader(t, agenttest.Output{Hang: true})
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- observeLeaderExit(pid) }()

	waittest.Never(t, done, darwinExitProbeInterval+500*time.Millisecond, "observeLeaderExit() while the direct child still runs")

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("cmd.Process.Kill() = %v", err)
	}
	awaitObservation(t, done, pid)
	if err := cmd.Wait(); !killedBySignal(err) {
		t.Errorf("cmd.Wait() = %v, want the signal status of a child that stayed unreaped", err)
	}
}

func TestObserveLeaderExitReturnsForExitAfterAndBeforeCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		exitBefore bool
	}{
		{name: "exit after the call began", exitBefore: false},
		{name: "exit before the call", exitBefore: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := startLeader(t, agenttest.Output{Hang: true})
			pid := cmd.Process.Pid
			done := make(chan error, 1)
			if tt.exitBefore {
				awaitExitUnreaped(t, cmd)
				go func() { done <- observeLeaderExit(pid) }()
			} else {
				go func() { done <- observeLeaderExit(pid) }()
				if err := cmd.Process.Kill(); err != nil {
					t.Fatalf("cmd.Process.Kill() = %v", err)
				}
			}

			awaitObservation(t, done, pid)

			if err := cmd.Wait(); !killedBySignal(err) {
				t.Errorf("cmd.Wait() = %v, want the signal status of a child that stayed unreaped", err)
			}
		})
	}
}

func TestZombieProbeReadsExitedAndRunning(t *testing.T) {
	t.Parallel()

	cmd := startLeader(t, agenttest.Output{Hang: true})
	pid := cmd.Process.Pid

	running, err := isZombie(pid)
	if err != nil {
		t.Fatalf("isZombie(%d) while running = %v", pid, err)
	}
	if running {
		t.Errorf("isZombie(%d) = true for a running direct child, want false", pid)
	}

	awaitExitUnreaped(t, cmd)

	exited, err := isZombie(pid)
	if err != nil {
		t.Fatalf("isZombie(%d) after the exit = %v", pid, err)
	}
	if !exited {
		t.Errorf("isZombie(%d) = false for an exited, unreaped direct child, want true", pid)
	}
}

func TestHasLiveMemberZombieOnly(t *testing.T) {
	t.Parallel()

	cmd := startLeader(t, agenttest.Output{Hang: true})
	pid := cmd.Process.Pid
	awaitExitUnreaped(t, cmd)

	live, err := hasLiveMember(pid, pid)
	if err != nil {
		t.Fatalf("hasLiveMember(%d, %d) = %v", pid, pid, err)
	}
	if live {
		t.Errorf("hasLiveMember(%d, %d) = true for a group holding only the exited, unreaped direct child, want false", pid, pid)
	}
}

func TestHasLiveMemberOneOtherMember(t *testing.T) {
	t.Parallel()

	cmd := startLeaderWithLiveMember(t)
	pid := cmd.Process.Pid

	live, err := hasLiveMember(pid, pid)
	if err != nil {
		t.Fatalf("hasLiveMember(%d, %d) = %v", pid, pid, err)
	}
	if !live {
		t.Errorf("hasLiveMember(%d, %d) = false while a descendant runs in the group, want true", pid, pid)
	}
}

func TestKillGroupZeroSignalEPERMForZombieOnlyGroup(t *testing.T) {
	t.Parallel()

	cmd := startLeader(t, agenttest.Output{Hang: true})
	pid := cmd.Process.Pid
	awaitExitUnreaped(t, cmd)

	err := syscall.Kill(-pid, 0)

	if !errors.Is(err, syscall.EPERM) {
		t.Errorf("syscall.Kill(-%d, 0) = %v for a group holding only the exited, unreaped direct child, want EPERM", pid, err)
	}
}
