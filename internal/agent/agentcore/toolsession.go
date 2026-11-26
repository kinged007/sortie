package agentcore

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/mcpconfig"
	"github.com/sortie-ai/sortie/internal/agent/procutil"
	"github.com/sortie-ai/sortie/internal/agent/sshutil"
	"github.com/sortie-ai/sortie/internal/agent/toolrelay"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/registry"
)

// OpeningNotice declares a session's opening capability gaps through emit,
// which bypasses the [ToolSession.Emit] wrapper. toolServersGap reports
// whether the session lacks Sortie's tool servers or withheld a configured one.
type OpeningNotice func(emit func(domain.AgentEvent), toolServersGap bool)

// PlaceFunc puts a dialect's finished rendering on a remote launch: the agent
// arguments and late environment entries that carry it.
type PlaceFunc func(d mcpconfig.Delivery) sshutil.LatePreamble

// ToolSession is one session's tool-server delivery, local or remote. It is
// safe for concurrent use; Close is idempotent.
type ToolSession struct {
	logger      *slog.Logger
	dialect     registry.MCPDialect
	remote      bool
	configured  bool
	stopGrace   time.Duration
	readTimeout time.Duration

	launch    mcpconfig.StdioLaunch
	hasLaunch bool

	mu         sync.Mutex // guards the fields below up to localOnce
	relay      *toolrelay.Relay
	config     mcpconfig.Config
	withheld   bool
	startSeen  bool
	noticeDone bool
	rendered   bool
	noHTTP     bool

	localOnce sync.Once
}

// placeholderEndpoint lets a remote session check that its servers render
// before the real remote port is known.
var placeholderEndpoint = mcpconfig.Endpoint{URL: "http://127.0.0.1:1/mcp", Bearer: "placeholder"}

// ResolveToolDelivery loads params.MCPConfigPath, when set, and returns the
// session's tool-server delivery in dialect. A configuration that fails to
// load or render returns [domain.ErrResponseError]; ctx ending during the
// remote startup probe returns the session-start cancellation.
//
// The result is non-nil when the error is nil; the caller closes it on every
// later error path.
func ResolveToolDelivery(ctx context.Context, params domain.StartSessionParams, target LaunchTarget, dialect registry.MCPDialect, logger *slog.Logger) (*ToolSession, *domain.AgentError) {
	if logger == nil {
		logger = slog.Default()
	}
	session := &ToolSession{
		logger:      logger,
		dialect:     dialect,
		remote:      target.RemoteCommand != "",
		configured:  params.MCPConfigPath != "",
		stopGrace:   procutil.StopGrace(params.AgentConfig.StopGraceMS),
		readTimeout: ReadTimeout(params.AgentConfig),
	}
	if !session.configured {
		return session, nil
	}

	loaded, err := mcpconfig.Load(params.MCPConfigPath)
	if err != nil {
		return nil, &domain.AgentError{
			Kind:    domain.ErrResponseError,
			Message: fmt.Sprintf("parse MCP config: %v", err),
			Err:     err,
		}
	}
	if !params.CredentialVerification {
		session.launch, session.hasLaunch = loaded.ToolServer()
	}

	switch {
	case !session.remote:
		session.config = loaded
	case session.hasLaunch:
		session.startRemote(ctx, loaded)
	}

	var placeholder *mcpconfig.Endpoint
	if session.remote {
		placeholder = &placeholderEndpoint
	}
	if _, err := session.render(session.config, placeholder); err != nil {
		session.Close() //nolint:errcheck,gosec // best-effort; the session start fails anyway
		return nil, &domain.AgentError{
			Kind:    domain.ErrResponseError,
			Message: fmt.Sprintf("render MCP servers: %v", err),
			Err:     err,
		}
	}

	if relay := session.currentRelay(); relay != nil && relay.HostPort() != 0 {
		if agentErr := session.probe(ctx, target); agentErr != nil {
			session.Close() //nolint:errcheck,gosec // best-effort; the session start fails anyway
			return nil, agentErr
		}
	}
	return session, nil
}

// startRemote delivers operator HTTP servers only when the relay announced
// its listener: only then does a launch carry a forward to declare them
// through.
func (s *ToolSession) startRemote(ctx context.Context, loaded mcpconfig.Config) {
	relay := toolrelay.Start(ctx, toolrelay.StartParams{
		ToolServer: s.launch,
		StopGrace:  s.stopGrace,
		Logger:     s.logger,
	})
	s.relay = relay

	delivered, withheld := loaded.WithoutTransport(mcpconfig.TransportStdio)
	if relay.HostPort() == 0 {
		var withheldHTTP []string
		delivered, withheldHTTP = delivered.WithoutTransport(mcpconfig.TransportHTTP)
		withheld = append(withheld, withheldHTTP...)
	}
	s.config = delivered
	if len(withheld) > 0 {
		s.withheld = true
		s.logger.Warn("withheld tool servers from remote session", slog.Any("servers", withheld))
	}
}

// probe settles the forward before any runtime launches: one-process-per-turn
// kinds have no runtime to observe before their first prompt.
func (s *ToolSession) probe(ctx context.Context, target LaunchTarget) *domain.AgentError {
	opts := target.SSHOptions()
	opts.Env = nil
	opts.Forward = s.Forward(nil)
	launch := sshutil.BuildSSHLaunch(target.SSHHost, target.WorkspacePath, "true", nil, opts)

	cmd := exec.CommandContext(ctx, target.Command, launch.Args...) //nolint:gosec // args are constructed programmatically with shell quoting
	cmd.Stdin = launch.StdinReader()
	procutil.SetGroupCancel(cmd, s.stopGrace)
	if agentErr := target.BindWorkspace(cmd); agentErr != nil {
		return agentErr
	}

	pipes, _, err := procutil.StartWithOwnedPipes(ctx, cmd, s.logger)
	if err != nil {
		if cancelled := SessionStartCancelledError(ctx, err); cancelled != nil {
			return cancelled
		}
		s.currentRelay().Observe(sshutil.ForwardOutcome{Status: sshutil.ForwardUnconfirmed})
		return nil
	}
	defer pipes.Close() //nolint:errcheck,gosec // the process is reaped before return, so a close error cannot change the outcome

	reaper := procutil.StartReaper(cmd, s.logger)
	collector := procutil.NewStderrCollector(launch.WatchStderr(pipes.Stderr), s.logger)
	// An unread pipe would block a remote shell that prints on stdout.
	go io.Copy(io.Discard, pipes.Stdout) //nolint:errcheck,gosec // a read error only means the pipe closed, which is the end the copy waits for

	// The error is ctx's; it is reported after the killed process is reaped.
	_, _ = launch.WaitOutcome(ctx)
	<-reaper.Done()
	collector.FinishAndCollect(procutil.DefaultDrainGrace)
	return startCancelled(ctx)
}

func startCancelled(ctx context.Context) *domain.AgentError {
	if ctx.Err() == nil {
		return nil
	}
	return SessionStartCancelledError(ctx, &procutil.StartError{
		Stage:     procutil.StageProcessStart,
		Err:       ctx.Err(),
		Cancelled: true,
	})
}

func (s *ToolSession) currentRelay() *toolrelay.Relay {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.relay
}

func (s *ToolSession) currentConfig() mcpconfig.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config
}

func (s *ToolSession) render(cfg mcpconfig.Config, ep *mcpconfig.Endpoint) (mcpconfig.Delivery, error) {
	return cfg.Render(s.dialect, mcpconfig.RenderInput{
		ToolServer: ep,
		BaseEnv:    append(os.Environ(), cfg.HeaderEnv()...),
		Logger:     s.renderLogger(),
	})
}

// renderLogger logs render warnings once per session: later renders only
// shrink the server set, so they cannot surface a new warning.
func (s *ToolSession) renderLogger() *slog.Logger {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rendered {
		return slog.New(slog.DiscardHandler)
	}
	s.rendered = true
	return s.logger
}

// usableEndpoint returns the endpoint a runtime reaches Sortie's tool server
// at for outcome o, or nil when none is usable.
func (s *ToolSession) usableEndpoint(o sshutil.ForwardOutcome) *mcpconfig.Endpoint {
	s.mu.Lock()
	relay, noHTTP := s.relay, s.noHTTP
	s.mu.Unlock()
	if relay == nil || noHTTP {
		return nil
	}
	lookup := relay.LocalEndpoint
	if s.remote {
		lookup = func() (mcpconfig.Endpoint, bool) { return relay.EndpointFor(o) }
	}
	ep, ok := lookup()
	if !ok {
		return nil
	}
	return &ep
}

// Delivery renders the session's current servers in the kind's dialect. A
// local caller passes the zero outcome.
//
// The configuration already rendered at resolve and later sets only shrink, so
// a render failure here is logged and yields an empty delivery.
func (s *ToolSession) Delivery(o sshutil.ForwardOutcome) mcpconfig.Delivery {
	delivery, err := s.render(s.currentConfig(), s.usableEndpoint(o))
	if err != nil {
		s.logger.Error("failed to render tool servers", slog.Any("error", err))
	}
	return delivery
}

// LocalEnv returns the header-value environment entries for a locally
// launched runtime.
func (s *ToolSession) LocalEnv() []string {
	return s.currentConfig().HeaderEnv()
}

// Forward returns the reverse forward a remote launch requests, or nil on a
// local launch or when the relay never announced its listener. A nil place
// renders nothing.
func (s *ToolSession) Forward(place PlaceFunc) *sshutil.ReverseForward {
	relay := s.currentRelay()
	if !s.remote || relay == nil {
		return nil
	}
	hostPort := relay.HostPort()
	if hostPort == 0 {
		return nil
	}
	return &sshutil.ReverseForward{
		HostPort: hostPort,
		Render: func(outcome sshutil.ForwardOutcome) sshutil.LatePreamble {
			return s.renderLate(place, outcome)
		},
		Resolved: relay.Observe,
	}
}

func (s *ToolSession) renderLate(place PlaceFunc, outcome sshutil.ForwardOutcome) sshutil.LatePreamble {
	if place == nil {
		return sshutil.LatePreamble{}
	}
	cfg := s.currentConfig()
	endpoint := s.usableEndpoint(outcome)
	delivery, err := s.render(cfg, endpoint)
	if err != nil {
		s.logger.Error("failed to render tool servers", slog.Any("error", err))
	}

	late := place(delivery)
	late.Env = slices.Clone(late.Env)
	for _, entry := range cfg.HeaderEnv() {
		name, value, _ := strings.Cut(entry, "=")
		late.Env = append(late.Env, sshutil.EnvVar{Name: name, Value: value})
	}
	if endpoint != nil {
		late.Env = append(late.Env, sshutil.EnvVar{Name: mcpconfig.BearerEnv, Value: endpoint.Bearer})
	}
	return late
}

// ServeLocal starts the relay for a local session whose runtime advertised
// HTTP tool-server support and reports whether its loopback endpoint is
// usable. From the first call on, the session's own stdio entry is no longer
// delivered.
func (s *ToolSession) ServeLocal(ctx context.Context) bool {
	if s.remote || !s.hasLaunch {
		return false
	}
	s.localOnce.Do(func() {
		relay := toolrelay.Start(ctx, toolrelay.StartParams{
			ToolServer: s.launch,
			StopGrace:  s.stopGrace,
			Logger:     s.logger,
		})
		s.mu.Lock()
		defer s.mu.Unlock()
		s.relay = relay
		s.config = s.config.WithoutToolServer()
	})
	_, ok := s.currentRelay().LocalEndpoint()
	return ok
}

// AwaitConnection waits, bounded by the read timeout, for the runtime's first
// connection to the tool server. It returns at once without a relay.
func (s *ToolSession) AwaitConnection(ctx context.Context) *domain.AgentError {
	relay := s.currentRelay()
	if relay == nil {
		return nil
	}
	if err := relay.AwaitConnection(ctx, s.readTimeout); err != nil {
		return startCancelled(ctx)
	}
	return nil
}

// WithholdForRuntime records that the runtime accepts no HTTP server and
// reports whether that withheld a configured server or the relay's delivery.
func (s *ToolSession) WithholdForRuntime() bool {
	s.mu.Lock()
	s.noHTTP = true
	kept, removed := s.config.WithoutTransport(mcpconfig.TransportHTTP)
	s.config = kept
	if len(removed) > 0 {
		s.withheld = true
	}
	relay := s.relay
	s.mu.Unlock()

	withheldRelay := false
	if s.remote && relay != nil {
		withheldRelay = decisionPending(relay.Decision())
		relay.WithholdForRuntime()
	}
	return len(removed) > 0 || withheldRelay
}

// Configured reports whether the session has a tool-server configuration path.
func (s *ToolSession) Configured() bool {
	return s.configured
}

// NoToolChannel reports whether the relay decided the runtime cannot reach
// Sortie's tool server.
func (s *ToolSession) NoToolChannel() bool {
	relay := s.currentRelay()
	return relay != nil && relay.Decision().Gap != ""
}

// Emit returns an emit that forwards every event unchanged and adds the
// opening notice once per session: after the first started event when the
// delivery decision is final, otherwise before the first terminal event.
//
// A nil notice emits [toolrelay.GapNotice] on a tool-server gap. A nil session
// returns emit unchanged.
func (s *ToolSession) Emit(emit func(domain.AgentEvent), notice OpeningNotice) func(domain.AgentEvent) {
	if s == nil {
		return emit
	}
	if notice == nil {
		notice = emitGapNotice
	}
	return func(event domain.AgentEvent) {
		switch {
		case isTerminalEvent(event.Type):
			if event.Type == domain.EventTurnCompleted {
				s.turnCompleted()
			}
			s.deliverNotice(emit, notice, false)
			emit(event)
		case event.Type == domain.EventSessionStarted:
			emit(event)
			s.deliverNotice(emit, notice, true)
		default:
			emit(event)
		}
	}
}

func emitGapNotice(emit func(domain.AgentEvent), toolServersGap bool) {
	if toolServersGap {
		EmitNotification(emit, toolrelay.GapNotice)
	}
}

func isTerminalEvent(eventType domain.AgentEventType) bool {
	switch eventType {
	case domain.EventTurnCompleted,
		domain.EventTurnFailed,
		domain.EventTurnCancelled,
		domain.EventTurnEndedWithError,
		domain.EventTurnInputRequired:
		return true
	default:
		return false
	}
}

func (s *ToolSession) turnCompleted() {
	if relay := s.currentRelay(); relay != nil {
		relay.TurnCompleted()
	}
}

// deliverNotice runs notice unlocked, so it may call back into s.
func (s *ToolSession) deliverNotice(emit func(domain.AgentEvent), notice OpeningNotice, atStart bool) {
	toolServersGap, claimed := s.claimNotice(atStart)
	if claimed {
		notice(emit, toolServersGap)
	}
}

func (s *ToolSession) claimNotice(atStart bool) (toolServersGap, claimed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if atStart {
		first := !s.startSeen
		s.startSeen = true
		if !first || (s.relay != nil && decisionPending(s.relay.Decision())) {
			return false, false
		}
	}
	if s.noticeDone {
		return false, false
	}
	s.noticeDone = true
	return s.withheld || (s.relay != nil && s.relay.Decision().Gap != ""), true
}

func decisionPending(decision toolrelay.Decision) bool {
	return !decision.Delivered && decision.Gap == ""
}

// Close closes the relay, if any. It is idempotent and nil-safe.
func (s *ToolSession) Close() error {
	if s == nil {
		return nil
	}
	if relay := s.currentRelay(); relay != nil {
		return relay.Close()
	}
	return nil
}
