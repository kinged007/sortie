package toolrelay

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/agent/mcpconfig"
	"github.com/sortie-ai/sortie/internal/agent/sshutil"
	"github.com/sortie-ai/sortie/internal/redact"
	"github.com/sortie-ai/sortie/internal/waittest"
)

const (
	announcedAddr = "127.0.0.1:43210"
	awaitBound    = time.Minute
)

func startChildRelay(t *testing.T, p childParams, env map[string]string) *Relay {
	t.Helper()

	command := agenttest.FakeRuntime(t, t.TempDir(), "toolserver", childScenario, p)
	return startRelay(t, mcpconfig.StdioLaunch{Command: command, Env: env})
}

func startRelay(t *testing.T, entry mcpconfig.StdioLaunch) *Relay {
	t.Helper()

	r := Start(waittest.CleanupContext(t), StartParams{
		ToolServer: entry,
		StopGrace:  waittest.HangGuard,
		Logger:     slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("Close() = %v, want nil", err)
		}
	})
	return r
}

func runningRelay(t *testing.T) *Relay {
	t.Helper()

	return startChildRelay(t, childParams{Announce: announcedAddr}, nil)
}

func tell(t *testing.T, r *Relay, command string) {
	t.Helper()

	if _, err := io.WriteString(r.stdin, command+"\n"); err != nil {
		t.Fatalf("write %q to child stdin: %v", command, err)
	}
}

func deliver(t *testing.T, r *Relay) {
	t.Helper()

	tell(t, r, childConnect)
	waittest.Until(t, func() error {
		if !r.Decision().Delivered {
			return errors.New("decision not delivered yet")
		}
		return nil
	})
}

func feedThenDrain(commands ...string) func(*testing.T, *Relay) {
	return func(t *testing.T, r *Relay) {
		t.Helper()

		for _, command := range commands {
			tell(t, r, command)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("Close() = %v, want nil", err)
		}
	}
}

func exitChild(t *testing.T, r *Relay) {
	t.Helper()

	tell(t, r, childExit)
	waittest.Receive(t, r.exitedCh, "child exit")
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	return string(waittest.File(t, path))
}

func TestDecisionTable(t *testing.T) {
	t.Parallel()

	refused := sshutil.ForwardOutcome{Status: sshutil.ForwardRefused}
	unconfirmed := sshutil.ForwardOutcome{Status: sshutil.ForwardUnconfirmed}
	allocated := sshutil.ForwardOutcome{Status: sshutil.ForwardAllocated, RemotePort: 2222}

	missingCommand := func(t *testing.T) *Relay {
		return startRelay(t, mcpconfig.StdioLaunch{Command: filepath.Join(t.TempDir(), "absent")})
	}
	exitsBeforeAnnouncing := func(t *testing.T) *Relay {
		return startChildRelay(t, childParams{}, nil)
	}
	contextEnded := func(t *testing.T) *Relay {
		command := agenttest.FakeRuntime(t, t.TempDir(), "toolserver", childScenario, childParams{Announce: announcedAddr})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := Start(ctx, StartParams{ToolServer: mcpconfig.StdioLaunch{Command: command}, Logger: slog.New(slog.DiscardHandler)})
		t.Cleanup(func() { r.Close() }) //nolint:errcheck,gosec // result is irrelevant for a relay that never ran
		return r
	}
	afterDelivered := func(steps ...func(*testing.T, *Relay)) func(*testing.T, *Relay) {
		return func(t *testing.T, r *Relay) {
			deliver(t, r)
			for _, step := range steps {
				step(t, r)
			}
		}
	}
	observe := func(o sshutil.ForwardOutcome) func(*testing.T, *Relay) {
		return func(_ *testing.T, r *Relay) { r.Observe(o) }
	}
	withhold := func(_ *testing.T, r *Relay) { r.WithholdForRuntime() }
	turnCompleted := func(_ *testing.T, r *Relay) { r.TurnCompleted() }

	tests := []struct {
		name  string
		start func(*testing.T) *Relay
		act   func(*testing.T, *Relay)
		want  Decision
	}{
		{name: "pending at start", start: runningRelay},
		{name: "connected event", start: runningRelay, act: deliver, want: Decision{Delivered: true}},
		{name: "start with missing command", start: missingCommand, want: Decision{Gap: GapRelayUnavailable}},
		{name: "child exits before announcing", start: exitsBeforeAnnouncing, want: Decision{Gap: GapRelayUnavailable}},
		{name: "context ended before start", start: contextEnded, want: Decision{Gap: GapRelayUnavailable}},
		{name: "child exits while pending", start: runningRelay, act: exitChild, want: Decision{Gap: GapRelayUnavailable}},
		{name: "child exits after delivery", start: runningRelay, act: afterDelivered(exitChild), want: Decision{Delivered: true, Gap: GapRelayUnavailable}},
		{name: "forward refused while pending", start: runningRelay, act: observe(refused), want: Decision{Gap: GapForwardRefused}},
		{name: "forward refused after delivery", start: runningRelay, act: afterDelivered(observe(refused)), want: Decision{Delivered: true, Gap: GapForwardRefused}},
		{name: "forward unconfirmed while pending", start: runningRelay, act: observe(unconfirmed), want: Decision{Gap: GapForwardUnconfirmed}},
		{name: "forward allocated while pending", start: runningRelay, act: observe(allocated)},
		{name: "withheld while pending", start: runningRelay, act: withhold, want: Decision{Gap: GapRuntimeWithoutHTTP}},
		{name: "turn completed while pending", start: runningRelay, act: turnCompleted, want: Decision{Gap: GapNoConnection}},
		{name: "accept refused while pending", start: runningRelay, act: feedThenDrain(childRefuseAccept), want: Decision{Gap: GapAcceptRefused}},
		{name: "connected after gap", start: runningRelay, act: func(t *testing.T, r *Relay) {
			observe(refused)(t, r)
			feedThenDrain(childConnect)(t, r)
		}, want: Decision{Gap: GapForwardRefused}},
		{
			name:  "first gap is kept",
			start: runningRelay,
			act: func(t *testing.T, r *Relay) {
				observe(refused)(t, r)
				observe(unconfirmed)(t, r)
				withhold(t, r)
				turnCompleted(t, r)
				exitChild(t, r)
			},
			want: Decision{Gap: GapForwardRefused},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := tt.start(t)
			if tt.act != nil {
				tt.act(t, r)
			}

			if got := r.Decision(); got != tt.want {
				t.Errorf("Decision() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStartBearer(t *testing.T) {
	t.Parallel()

	bearerFile := filepath.Join(t.TempDir(), "bearer")

	r := startChildRelay(t, childParams{Announce: announcedAddr, BearerFile: bearerFile}, map[string]string{mcpconfig.BearerEnv: "declared"})

	got := readFile(t, bearerFile)
	ep, _ := r.LocalEndpoint()
	if got == "declared" || got != ep.Bearer {
		t.Errorf("child %s = %q, want the relay bearer %q", mcpconfig.BearerEnv, got, ep.Bearer)
	}
	if raw, err := hex.DecodeString(ep.Bearer); err != nil || len(raw) != 32 || ep.Bearer != strings.ToLower(ep.Bearer) {
		t.Errorf("bearer = %q, want 32 bytes of lowercase hex", ep.Bearer)
	}
	if masked := redact.Mask(ep.Bearer); masked == ep.Bearer {
		t.Errorf("redact.Mask(bearer) = %q, want the bearer masked", masked)
	}
}

func TestCloseEndsChild(t *testing.T) {
	t.Parallel()

	pidFile := filepath.Join(t.TempDir(), "pid")
	r := startChildRelay(t, childParams{Announce: announcedAddr, PIDFile: pidFile}, nil)
	pid := waittest.PIDFile(t, pidFile)

	err := r.Close()
	again := r.Close()

	if err != nil || again != nil {
		t.Fatalf("Close() = %v, then %v, want nil twice", err, again)
	}
	waittest.ProcessGone(t, pid)
	if got := r.Decision(); got != (Decision{}) {
		t.Errorf("Decision() after Close = %+v, want pending", got)
	}
	if ep, ok := r.LocalEndpoint(); ok {
		t.Errorf("LocalEndpoint() after Close = %+v, true, want false", ep)
	}
	allocated := sshutil.ForwardOutcome{Status: sshutil.ForwardAllocated, RemotePort: 2222}
	if ep, ok := r.EndpointFor(allocated); ok {
		t.Errorf("EndpointFor(%+v) after Close = %+v, true, want false", allocated, ep)
	}
}

func newBareRelay() *Relay {
	return &Relay{logger: slog.New(slog.DiscardHandler), settled: make(chan struct{})}
}

func TestAwaitConnection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		trigger      func(r *Relay, cancel context.CancelFunc)
		wantErr      error
		wantDecision Decision
		wantElapsed  time.Duration
	}{
		{"connected event", func(r *Relay, _ context.CancelFunc) { r.connectionObserved() }, nil, Decision{Delivered: true}, 0},
		{"bound reached", nil, nil, Decision{Gap: GapNoConnection}, awaitBound},
		{"context ends first", func(_ *Relay, cancel context.CancelFunc) { cancel() }, context.Canceled, Decision{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				r := newBareRelay()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				start := time.Now()
				got := make(chan error, 1)
				go func() { got <- r.AwaitConnection(ctx, awaitBound) }()
				synctest.Wait()
				select {
				case err := <-got:
					t.Fatalf("AwaitConnection() = %v before its trigger, want to block", err)
				default:
				}

				if tt.trigger != nil {
					tt.trigger(r, cancel)
				}
				err := <-got

				if !errors.Is(err, tt.wantErr) {
					t.Errorf("AwaitConnection() = %v, want %v", err, tt.wantErr)
				}
				if r.Decision() != tt.wantDecision {
					t.Errorf("Decision() = %+v, want %+v", r.Decision(), tt.wantDecision)
				}
				if elapsed := time.Since(start); elapsed != tt.wantElapsed {
					t.Errorf("AwaitConnection() returned after %v, want %v", elapsed, tt.wantElapsed)
				}
			})
		})
	}
}
