// Package toolrelay runs one tool server per session on the orchestrator
// host and owns the session's delivery decision: whether the runtime
// reached that server, or why it did not.
//
// The server is the generated stdio tool-server entry started with its
// HTTP flag, so the runtime on a remote host reaches it through a
// forwarded loopback port and a local runtime reaches it over loopback.
// The relay learns from the child's standard output whether a runtime
// connected; it never sees a request itself.
package toolrelay

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/mcpconfig"
	"github.com/sortie-ai/sortie/internal/agent/procutil"
	"github.com/sortie-ai/sortie/internal/agent/sshutil"
	"github.com/sortie-ai/sortie/internal/redact"
	"github.com/sortie-ai/sortie/internal/tool/mcpserver"
)

const (
	// GapNoticeStem opens the notice a session emits when it started with
	// a declared capability gap; the labels of the gaps follow it.
	GapNoticeStem = "this session started with a declared capability gap in: "

	// ToolServersLabel names the tool-server gap in a [GapNoticeStem]
	// notice.
	ToolServersLabel = "tool servers"

	// GapNotice is the whole notice for a session whose only gap is the
	// tool servers.
	GapNotice = GapNoticeStem + ToolServersLabel
)

const (
	httpFlag     = "--http"
	loopbackHost = "127.0.0.1"
	bearerBytes  = 32

	// refusedAccept is the HTTPEvent.Refused value for a request whose
	// Accept header the tool server could not serve.
	refusedAccept = "accept"
)

// GapReason names why a session's runtime did not receive Sortie's tool
// server. The set is closed.
type GapReason string

const (
	// GapRelayUnavailable means the relay's child failed to start, exited
	// before announcing its listener, or exited mid-session.
	GapRelayUnavailable GapReason = "relay_unavailable"

	// GapForwardRefused means the remote host refused a launch's forward.
	GapForwardRefused GapReason = "forward_refused"

	// GapForwardUnconfirmed means a launch's forward outcome was neither
	// an allocation nor a refusal.
	GapForwardUnconfirmed GapReason = "forward_unconfirmed"

	// GapRuntimeWithoutHTTP means the runtime accepts no HTTP server.
	GapRuntimeWithoutHTTP GapReason = "runtime_without_http"

	// GapAcceptRefused means the tool server refused the first authorized
	// request because its Accept header named neither required media type.
	GapAcceptRefused GapReason = "accept_refused"

	// GapNoConnection means no runtime connected before the session's
	// connection deadline.
	GapNoConnection GapReason = "no_connection"
)

// Decision is a session's delivery decision. The zero value is pending.
// A non-empty Gap is final and outranks Delivered.
type Decision struct {
	// Delivered reports that a runtime connected to the tool server.
	Delivered bool

	// Gap is the first reason the session was declared to lack the tool
	// server, or empty.
	Gap GapReason
}

// StartParams configures [Start].
type StartParams struct {
	// ToolServer is the generated configuration's stdio tool-server
	// entry. Its Command, Args and Env launch the child.
	ToolServer mcpconfig.StdioLaunch

	// StopGrace is how long [Relay.Close] waits for the child to exit
	// after closing its standard input. A non-positive value selects
	// the procutil default.
	StopGrace time.Duration

	// Logger receives the relay's records. Nil selects slog.Default.
	Logger *slog.Logger
}

// Relay is one session's tool server and delivery decision. It is safe
// for concurrent use.
type Relay struct {
	logger    *slog.Logger
	bearer    string
	stopGrace time.Duration

	// mu guards every field below it up to the channels. A channel is
	// closed by the goroutine that owns the transition, never twice.
	mu        sync.Mutex
	decision  Decision
	connected bool
	hostPort  int
	exited    bool
	closing   bool

	settled      chan struct{} // closed when the decision leaves pending
	exitedCh     chan struct{} // closed when the child has been reaped
	finished     chan struct{} // closed when every relay goroutine has ended
	announceOnce sync.Once
	announced    chan struct{}

	stdin io.WriteCloser
	group *procutil.Group

	closeOnce sync.Once
	closeErr  error
}

// Start launches the tool server child and returns once it announced its
// listener, exited, or ctx ended, which kills it. A
// child that fails yields a Relay whose decision is [GapRelayUnavailable]
// and whose HostPort is 0. The caller closes the Relay either way.
func Start(ctx context.Context, p StartParams) *Relay {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	grace := p.StopGrace
	if grace <= 0 {
		grace = procutil.StopGrace(0)
	}

	r := &Relay{
		logger:    logger.With(slog.String("component", "tool-relay")),
		stopGrace: grace,
		settled:   make(chan struct{}),
		exitedCh:  make(chan struct{}),
		finished:  make(chan struct{}),
		announced: make(chan struct{}),
	}

	bearer, err := newBearer()
	if err != nil {
		return r.neverStarted(fmt.Errorf("create bearer: %w", err))
	}
	redact.Add("tool server bearer", bearer)
	r.bearer = bearer

	args := append(slices.Clone(p.ToolServer.Args), httpFlag)
	cmd := exec.CommandContext(ctx, p.ToolServer.Command, args...) //nolint:gosec // G204: the command is the generated tool-server entry, built by the orchestrator
	procutil.SetGroupCancel(cmd, grace)
	cmd.Env = childEnv(os.Environ(), p.ToolServer.Env, bearer)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return r.neverStarted(fmt.Errorf("create stdin pipe: %w", err))
	}
	pipes, group, err := procutil.StartWithOwnedPipes(ctx, cmd, r.logger)
	if err != nil {
		stdin.Close() //nolint:errcheck,gosec // best-effort; a failed launch leaves the write end open on a pipe-stage error
		return r.neverStarted(fmt.Errorf("start tool server: %w", err))
	}
	r.stdin = stdin
	r.group = group

	reaper := procutil.StartReaper(cmd, r.logger)
	collector := procutil.NewStderrCollector(pipes.Stderr, r.logger)
	readerDone := make(chan struct{})
	go r.readEvents(pipes.Stdout, readerDone)
	procutil.StartOutputRelease(procutil.OutputReleaseParams{
		Pipes:      pipes,
		Reaped:     reaper.Done(),
		ReaderDone: readerDone,
		Stderr:     collector,
		Logger:     r.logger,
	})
	go r.supervise(ctx, reaper, collector, pipes, readerDone)

	select {
	case <-r.announced:
	case <-r.exitedCh:
	case <-ctx.Done():
	}

	if r.HostPort() == 0 {
		r.failStart(ctx)
	}
	return r
}

// neverStarted returns r as a relay whose child never ran.
func (r *Relay) neverStarted(cause error) *Relay {
	r.mu.Lock()
	r.exited = true
	r.setGapLocked(GapRelayUnavailable, slog.Any("error", cause))
	r.mu.Unlock()
	redact.Remove(r.bearer)
	close(r.exitedCh)
	close(r.finished)
	return r
}

// failStart records the failed start and kills a child that is still
// running, so no relay goroutine outlives a child that will never serve.
func (r *Relay) failStart(ctx context.Context) {
	cause := errors.New("tool server child did not announce its listener")
	if ctx.Err() != nil {
		cause = ctx.Err()
	}
	r.mu.Lock()
	r.setGapLocked(GapRelayUnavailable, slog.Any("error", cause))
	r.mu.Unlock()
	r.group.Kill() //nolint:errcheck,gosec // best-effort; a child that is already gone needs no kill
}

// HostPort returns the child's loopback port, or 0 when the child never
// announced its listener.
func (r *Relay) HostPort() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hostPort
}

// Decision returns the session's delivery decision.
func (r *Relay) Decision() Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.decision
}

// Observe feeds the outcome of a launch's forward into the decision: a
// refused or unconfirmed outcome declares a gap, an allocated one changes
// nothing.
func (r *Relay) Observe(o sshutil.ForwardOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch o.Status {
	case sshutil.ForwardRefused:
		r.setGapLocked(GapForwardRefused)
	case sshutil.ForwardUnconfirmed:
		r.setGapLocked(GapForwardUnconfirmed)
	}
}

// WithholdForRuntime declares that the runtime accepts no HTTP server. It
// changes only a pending decision.
func (r *Relay) WithholdForRuntime() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingLocked() {
		r.setGapLocked(GapRuntimeWithoutHTTP)
	}
}

// TurnCompleted declares that no runtime connected when a turn completed
// while the decision is still pending.
func (r *Relay) TurnCompleted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingLocked() && !r.connected {
		r.setGapLocked(GapNoConnection)
	}
}

// EndpointFor returns the endpoint a runtime reaches through a launch's
// forward, and true only for an allocated outcome while the child runs.
func (r *Relay) EndpointFor(o sshutil.ForwardOutcome) (mcpconfig.Endpoint, bool) {
	if o.Status != sshutil.ForwardAllocated || o.RemotePort < 1 || o.RemotePort > 65535 {
		return mcpconfig.Endpoint{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.runningLocked() {
		return mcpconfig.Endpoint{}, false
	}
	return mcpconfig.Endpoint{URL: endpointURL(o.RemotePort), Bearer: r.bearer}, true
}

// LocalEndpoint returns the endpoint a runtime on the orchestrator host
// reaches, and true while the child runs.
func (r *Relay) LocalEndpoint() (mcpconfig.Endpoint, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.runningLocked() {
		return mcpconfig.Endpoint{}, false
	}
	return mcpconfig.Endpoint{URL: endpointURL(r.hostPort), Bearer: r.bearer}, true
}

// AwaitConnection blocks until the decision leaves pending, and returns
// nil. When bound passes first it declares [GapNoConnection] and returns
// nil. It returns ctx.Err() when ctx ends first.
func (r *Relay) AwaitConnection(ctx context.Context, bound time.Duration) error {
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case <-r.settled:
		return nil
	case <-timer.C:
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.pendingLocked() {
			r.setGapLocked(GapNoConnection)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close closes the child's standard input, which ends its serving, waits
// up to the stop grace for it to exit, then kills its process group. It
// is idempotent: every call returns the first call's result.
func (r *Relay) Close() error {
	r.closeOnce.Do(func() { r.closeErr = r.shutdown() })
	return r.closeErr
}

func (r *Relay) shutdown() error {
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()

	if r.stdin != nil {
		r.stdin.Close() //nolint:errcheck,gosec // best-effort; an already-closed pipe means the child is gone
	}

	grace := time.NewTimer(r.stopGrace)
	defer grace.Stop()
	select {
	case <-r.finished:
		return nil
	case <-grace.C:
	}

	r.logger.Warn("tool server did not exit inside the graceful period and was force-terminated",
		slog.Duration("grace", r.stopGrace))
	killErr := r.group.Kill()

	bound := time.NewTimer(r.stopGrace)
	defer bound.Stop()
	select {
	case <-r.finished:
		return killErr
	case <-bound.C:
		return errors.Join(killErr, errors.New("tool server child did not exit after being killed"))
	}
}

func (r *Relay) pendingLocked() bool {
	return !r.decision.Delivered && r.decision.Gap == ""
}

func (r *Relay) runningLocked() bool {
	return r.hostPort != 0 && !r.exited
}

// setGapLocked declares a gap unless one is already declared: the first
// reason is the one operators see, and later ones are consequences of it.
func (r *Relay) setGapLocked(reason GapReason, attrs ...slog.Attr) {
	if r.decision.Gap != "" {
		return
	}
	r.decision.Gap = reason
	r.logger.LogAttrs(context.Background(), slog.LevelWarn, "declared tool server gap for session",
		append([]slog.Attr{slog.String("reason", string(reason))}, attrs...)...)
	r.settleLocked()
}

func (r *Relay) settleLocked() {
	select {
	case <-r.settled:
	default:
		close(r.settled)
	}
}

func (r *Relay) connectionObserved() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connected = true
	if !r.pendingLocked() {
		return
	}
	r.decision.Delivered = true
	r.logger.Info("delivered tool server to session")
	r.settleLocked()
}

// acceptRefused declares the gap of a refused Accept header. It changes only
// a pending decision: a delivered session already proved a request got through.
func (r *Relay) acceptRefused(accept string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingLocked() {
		r.setGapLocked(GapAcceptRefused, slog.String("accept", accept))
	}
}

// readEvents reads the child's standard output, one HTTP event per line.
// A line that is not an event is ignored: the child owns its output and
// a stray line must not end the relay.
func (r *Relay) readEvents(out io.Reader, done chan<- struct{}) {
	defer close(done)
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64*1024), procutil.DefaultScannerMaxSize)
	for scanner.Scan() {
		var ev mcpserver.HTTPEvent
		if json.Unmarshal(scanner.Bytes(), &ev) != nil {
			r.logger.Debug("ignored tool server output line that is not an event")
			continue
		}
		if ev.Listening != "" {
			r.announce(ev.Listening)
		}
		if ev.Connected {
			r.connectionObserved()
		}
		if ev.Refused == refusedAccept {
			r.acceptRefused(ev.Accept)
		}
	}
	if err := scanner.Err(); err != nil {
		r.logger.Debug("stopped reading tool server output", slog.Any("error", err))
	}
}

// announce records the child's listener once, when its address is a
// loopback port.
func (r *Relay) announce(addr string) {
	host, portText, err := net.SplitHostPort(addr)
	port, convErr := strconv.Atoi(portText)
	if err != nil || convErr != nil || host != loopbackHost || port < 1 || port > 65535 {
		r.logger.Warn("tool server announced an unusable listener address")
		return
	}
	r.announceOnce.Do(func() {
		r.mu.Lock()
		r.hostPort = port
		r.mu.Unlock()
		close(r.announced)
	})
}

// supervise waits for the child to exit, records an unexpected exit, and
// closes the child's pipes once both readers ended. The output release
// started beside it bounds those readers, so the waits here always end.
// It is the last relay goroutine to end.
func (r *Relay) supervise(ctx context.Context, reaper *procutil.Reaper, collector *procutil.StderrCollector, pipes *procutil.OwnedPipes, readerDone <-chan struct{}) {
	defer close(r.finished)
	defer redact.Remove(r.bearer)
	<-reaper.Done()
	exitCode := procutil.ExtractExitCode(reaper.Err())

	r.mu.Lock()
	r.exited = true
	unexpected := !r.closing && ctx.Err() == nil
	if unexpected {
		r.setGapLocked(GapRelayUnavailable, slog.Int("exit_code", exitCode))
	}
	r.mu.Unlock()
	close(r.exitedCh)

	if unexpected {
		lines, _ := collector.LastLines()
		procutil.EmitWarnLines(lines, r.logger)
	}
	<-readerDone
	<-collector.Done()
	pipes.Close() //nolint:errcheck,gosec // both readers have ended, so a failed close leaves no data to lose
}

func endpointURL(port int) string {
	return "http://" + net.JoinHostPort(loopbackHost, strconv.Itoa(port)) + "/mcp"
}

func newBearer() (string, error) {
	b := make([]byte, bearerBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// childEnv overlays the entry's environment on base and appends the
// bearer last. exec.Cmd keeps the last value of a duplicated key, so an
// inherited or declared variable of the same name never wins.
func childEnv(base []string, overlay map[string]string, bearer string) []string {
	env := slices.Clone(base)
	keys := make([]string, 0, len(overlay))
	for k := range overlay {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		env = append(env, k+"="+overlay[k])
	}
	return append(env, mcpconfig.BearerEnv+"="+bearer)
}
