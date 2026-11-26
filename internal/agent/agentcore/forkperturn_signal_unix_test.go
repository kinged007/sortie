//go:build unix

package agentcore

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/agent/procutil"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/waittest"
)

// The marker exists because a fixed sleep cannot prove the trap is installed:
// if the signal wins that race the process exits on the platform's default
// disposition and the test passes without exercising the escalation it names.
func buildTrapAgent(t *testing.T, dir string, exitOnSignal bool) (path, marker string) {
	t.Helper()
	marker = filepath.Join(dir, "trap-ready")
	path = agenttest.FakeRuntime(t, dir, "agent", "trap", trapParams{Marker: marker, ExitOnSignal: exitOnSignal})
	return path, marker
}

func TestForkPerTurnSession_GracefulSignal(t *testing.T) {
	t.Parallel()

	t.Run("Stop_ConfiguredGraceBoundsTheWait", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		script, ready := buildTrapAgent(t, tmpDir, false)
		target := newTestTarget(tmpDir, script)
		sess := NewForkPerTurnSession(target, noopHooks(), slog.Default(), 200)

		emit, _ := sinkEvents()
		done := make(chan struct{})
		go func() {
			defer close(done)
			sess.RunTurn(context.Background(), "p", emit) //nolint:errcheck // testing Stop's timing, not RunTurn's outcome
		}()

		waittest.File(t, ready)

		start := time.Now()
		if err := sess.Stop(context.Background()); err != nil {
			t.Errorf("Stop() = %v, want nil", err)
		}
		elapsed := time.Since(start)

		if elapsed > procutil.DefaultStopGrace {
			t.Errorf("Stop() force-terminated after %v, want under the built-in %v default (proves the configured 200ms grace bounded the wait, not DefaultStopGrace)", elapsed, procutil.DefaultStopGrace)
		}

		waittest.Receive(t, done, "RunTurn return after Stop")
	})

	t.Run("RunTurn_CancelledTurnEscalatesOnConfiguredGrace", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		// A signal that is received but not acted on, rather than a hang
		// with no signal handling at all: this proves the escalation path
		// force-kills a subprocess that ignores the graceful shutdown
		// signal, not merely one that never gets a chance to see it.
		script, ready := buildTrapAgent(t, tmpDir, false)
		target := newTestTarget(tmpDir, script)
		sess := NewForkPerTurnSession(target, noopHooks(), slog.Default(), 200)

		ctx, cancel := context.WithCancel(context.Background())
		emit, _ := sinkEvents()
		done := make(chan error, 1)
		go func() {
			_, err := sess.RunTurn(ctx, "p", emit)
			done <- err
		}()

		waittest.File(t, ready)
		start := time.Now()
		cancel()

		err := waittest.Receive(t, done, "RunTurn return after cancellation")
		elapsed := time.Since(start)

		requireAgentError(t, err, domain.ErrTurnCancelled)
		if elapsed > procutil.DefaultStopGrace {
			t.Errorf("RunTurn's cancelled-turn escalation took %v, want under the built-in %v default (proves the configured 200ms grace bounded cmd.WaitDelay, not DefaultStopGrace)", elapsed, procutil.DefaultStopGrace)
		}
	})

	t.Run("Stop_EscalationLogging", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			name         string
			exitOnSignal bool
			graceMS      int
			stopTimeout  time.Duration
			wantStopErr  error
			wantLogged   []string
			wantNoWarn   bool
		}{
			{
				name:         "exit_inside_grace_emits_debug_and_no_warn",
				exitOnSignal: true,
				// The grace is far longer than this exit needs. The case proves
				// which record the clean-exit path emits, not that the grace
				// bounds anything, and a tight bound races the runner's
				// scheduler instead of testing the code.
				graceMS:    30000,
				wantLogged: []string{"agent exited during the graceful phase", "outcome=exited"},
				wantNoWarn: true,
			},
			{
				name:    "grace_elapsed_emits_warn_with_outcome_and_grace",
				graceMS: 150,
				wantLogged: []string{
					"agent did not exit inside the graceful period and was force-terminated",
					`outcome="grace elapsed"`,
					"grace=",
					"elapsed=",
				},
			},
			{
				name:        "caller_deadline_emits_warn_with_that_outcome",
				graceMS:     5000,
				stopTimeout: 150 * time.Millisecond,
				wantStopErr: context.DeadlineExceeded,
				wantLogged:  []string{`outcome="caller deadline"`},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				tmpDir := t.TempDir()
				script, ready := buildTrapAgent(t, tmpDir, tc.exitOnSignal)
				target := newTestTarget(tmpDir, script)
				var buf bytes.Buffer
				logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
				sess := NewForkPerTurnSession(target, noopHooks(), logger, tc.graceMS)

				emit, _ := sinkEvents()
				done := make(chan struct{})
				go func() {
					defer close(done)
					sess.RunTurn(context.Background(), "p", emit) //nolint:errcheck // testing log output, not the outcome
				}()
				waittest.File(t, ready)

				stopCtx := context.Background()
				if tc.stopTimeout > 0 {
					var cancel context.CancelFunc
					stopCtx, cancel = context.WithTimeout(stopCtx, tc.stopTimeout)
					defer cancel()
				}
				if err := sess.Stop(stopCtx); !errors.Is(err, tc.wantStopErr) {
					t.Errorf("Stop() error = %v, want %v", err, tc.wantStopErr)
				}
				waittest.Receive(t, done, "RunTurn return after Stop")

				output := buf.String()
				for _, want := range tc.wantLogged {
					if !strings.Contains(output, want) {
						t.Errorf("Stop() log missing %s: %s", want, output)
					}
				}
				if tc.wantNoWarn && strings.Contains(output, "level=WARN") {
					t.Errorf("Stop() logged a Warn record for a clean exit, want none: %s", output)
				}
			})
		}
	})
}
