package procutil

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/waittest"
)

const (
	panicUnregisteredLaunch = "procutil: StartReaper requires a command started by a procutil start function"
	panicSecondReaper       = "procutil: StartReaper called twice for one launch"
)

func startOwned(t *testing.T, cmd *exec.Cmd) (*OwnedPipes, *Group) {
	t.Helper()
	pipes, g, err := StartWithOwnedPipes(context.Background(), cmd, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("StartWithOwnedPipes() error = %v, want nil", err)
	}
	t.Cleanup(func() { _ = pipes.Close() })
	return pipes, g
}

func terminateAndAwait(t *testing.T, cmd *exec.Cmd, g *Group, r *Reaper) {
	t.Helper()
	_ = g.Kill()
	_ = cmd.Process.Kill()
	waittest.Receive(t, r.Done(), "reaper Done()")
}

func panicMessage(fn func()) (msg string, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			msg, panicked = fmt.Sprint(r), true
		}
	}()
	fn()
	return "", false
}

func TestReaper_DoneAndErr(t *testing.T) {
	t.Parallel()

	t.Run("non-zero exit", func(t *testing.T) {
		t.Parallel()

		cmd := fakeRuntimeCmd(t, agenttest.Output{ExitCode: 7})
		startOwned(t, cmd)

		r := StartReaper(cmd, nil)
		waittest.Receive(t, r.Done(), "reaper Done()")

		var exitErr *exec.ExitError
		if !errors.As(r.Err(), &exitErr) {
			t.Fatalf("Err() = %v (%T), want *exec.ExitError", r.Err(), r.Err())
		}
		if got := exitErr.ExitCode(); got != 7 {
			t.Errorf("Err().(*exec.ExitError).ExitCode() = %d, want 7", got)
		}
	})

	t.Run("clean exit", func(t *testing.T) {
		t.Parallel()

		cmd := fakeRuntimeCmd(t, agenttest.Output{})
		startOwned(t, cmd)

		r := StartReaper(cmd, nil)
		waittest.Receive(t, r.Done(), "reaper Done()")

		if err := r.Err(); err != nil {
			t.Errorf("Err() = %v, want nil", err)
		}
	})

	t.Run("Done stays open while the subprocess is still running", func(t *testing.T) {
		t.Parallel()

		cmd := fakeRuntimeCmd(t, agenttest.Output{Hang: true})
		startOwned(t, cmd)

		r := StartReaper(cmd, nil)
		waittest.Never(t, r.Done(), 200*time.Millisecond, "Done() before the subprocess exited")

		if err := cmd.Process.Kill(); err != nil {
			t.Fatalf("cmd.Process.Kill() = %v", err)
		}
		waittest.Receive(t, r.Done(), "reaper Done()")
	})
}

func TestReaper_OutputSurvivesAfterDoneCloses(t *testing.T) {
	t.Parallel()

	cmd := fakeRuntimeCmd(t, agenttest.Output{Stdout: "hello from the child\n"})
	pipes, _ := startOwned(t, cmd)

	r := StartReaper(cmd, nil)
	waittest.Receive(t, r.Done(), "reaper Done()")

	line, err := bufio.NewReader(pipes.Stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read standard output after Done closed: %v", err)
	}
	if want := "hello from the child\n"; line != want {
		t.Errorf("standard output = %q, want %q", line, want)
	}
}

// A green result here would mean the positive case never raced the reap, and
// the fixture, not production code, would be what to fix.
func TestReaper_ClosingStdoutAtDoneLosesOutput(t *testing.T) {
	t.Parallel()

	cmd := fakeRuntimeCmd(t, agenttest.Output{Stdout: "hello from the child\n"})
	pipes, _ := startOwned(t, cmd)

	r := StartReaper(cmd, nil)
	waittest.Receive(t, r.Done(), "reaper Done()")
	if err := pipes.CloseStdout(); err != nil {
		t.Fatalf("CloseStdout() error = %v", err)
	}

	if line, err := bufio.NewReader(pipes.Stdout).ReadString('\n'); err == nil {
		t.Fatalf("read standard output after closing it at the reap = %q, want an error (the negative control did not reproduce the loss)", line)
	}
}

func TestStartReaperPanicsForPlainStart(t *testing.T) {
	t.Parallel()

	cmd := fakeRuntimeCmd(t, agenttest.Output{})
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start() = %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })

	msg, panicked := panicMessage(func() { StartReaper(cmd, nil) })

	if !panicked {
		t.Fatal("StartReaper(plain cmd.Start) did not panic, want a panic")
	}
	if msg != panicUnregisteredLaunch {
		t.Errorf("StartReaper(plain cmd.Start) panic = %q, want %q", msg, panicUnregisteredLaunch)
	}
}

func TestStartReaperPanicsOnSecondCall(t *testing.T) {
	t.Parallel()

	cmd := fakeRuntimeCmd(t, agenttest.Output{Hang: true})
	_, g := startOwned(t, cmd)
	first := StartReaper(cmd, nil)
	t.Cleanup(func() { terminateAndAwait(t, cmd, g, first) })

	msg, panicked := panicMessage(func() { StartReaper(cmd, nil) })

	if !panicked {
		t.Fatal("second StartReaper for one launch did not panic, want a panic")
	}
	if msg != panicSecondReaper {
		t.Errorf("second StartReaper panic = %q, want %q", msg, panicSecondReaper)
	}
}
