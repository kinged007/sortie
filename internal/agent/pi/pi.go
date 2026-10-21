// Package pi implements [domain.AgentAdapter] for the Pi CLI. It launches
// one `pi -p --mode json` subprocess per Sortie turn on the shared
// fork-per-turn skeleton, normalizes the JSON event stream into domain
// events, and settles the session's cumulative token usage from the usage
// figure each model response carries.
//
// The adapter declares [registry.MCPInjectionUnsupported]: it does not
// hand the worker-generated MCP configuration to pi, so Sortie's tools
// are not available to a pi session.
package pi

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/logging"
	"github.com/sortie-ai/sortie/internal/redact"
	"github.com/sortie-ai/sortie/internal/registry"
)

const defaultCommand = "pi"

func init() {
	registry.Agents.RegisterWithMeta("pi", NewPiAdapter, registry.AgentMeta{
		RequiresCommand:     true,
		DefaultCommand:      defaultCommand,
		ValidateAgentConfig: validateConfig,
		MCPInjection:        registry.MCPInjectionUnsupported,
		UsageArrival:        registry.UsageArrivalTurnEnd,
		UsageAttribution:    registry.UsageAttributionPerModel,
		CredentialEnv:       registry.DeclareCredentialEnv(),
		EffortForwarding:    registry.EffortNotForwarded,
	})
}

var _ domain.AgentAdapter = (*PiAdapter)(nil)

type PiAdapter struct{}

type sessionState struct {
	target                 agentcore.LaunchTarget
	agentConfig            domain.AgentConfig
	passthrough            passthroughConfig
	sessionID              string
	sessionOpened          bool
	baseLogger             *slog.Logger
	usage                  *agentcore.TurnEndUsage
	mu                     sync.Mutex
	credentialVerification bool

	// fork owns the turn subprocess lifecycle.
	fork *agentcore.ForkPerTurnSession

	// total and totalByModel are the run-cumulative usage of every turn
	// settled so far. Touched only from settleUsage.
	total        domain.TokenUsage
	totalByModel map[string]domain.TokenUsage

	// Per-turn state, reset by RunTurn and touched only from the hooks,
	// which run on RunTurn's goroutine.
	work  *agentcore.WorkObserver
	tools *agentcore.ToolTracker

	// turnUsage and turnByModel sum the turn's model responses. turnModel
	// names the model of the last response that carried usage, and
	// turnMeasured records that at least one response carried a usage
	// object, a zero one included.
	turnUsage    domain.TokenUsage
	turnByModel  map[string]domain.TokenUsage
	turnModel    string
	turnMeasured bool

	// sawMessageEnd records that the current pi turn already delivered its
	// message_end, so turn_end does not count the same response again.
	sawMessageEnd bool

	// lastStopReason and lastErrorMessage describe the last assistant
	// response. pi retries a response that stopped with "error" inside the
	// same process, so only the last one decides the turn.
	lastStopReason   string
	lastErrorMessage string

	agentEnded bool
}

// NewPiAdapter creates a [PiAdapter].
func NewPiAdapter() (domain.AgentAdapter, error) {
	return &PiAdapter{}, nil
}

// StartSession resolves the launch target and initializes adapter-owned
// session state without starting a turn subprocess.
func (a *PiAdapter) StartSession(_ context.Context, params domain.StartSessionParams) (domain.Session, error) {
	pt, fault := parsePassthroughConfig(params.Settings)
	if fault != nil {
		return domain.Session{}, agentcore.SettingsError(fault.Error(), fault)
	}

	target, agentErr := agentcore.ResolveLaunchTarget(params, defaultCommand)
	if agentErr != nil {
		return domain.Session{}, agentErr
	}

	state := &sessionState{
		target:                 target,
		agentConfig:            params.AgentConfig,
		passthrough:            pt,
		sessionID:              params.ResumeSessionID,
		baseLogger:             slog.Default().With(slog.String("component", "pi-adapter")),
		usage:                  agentcore.NewTurnEndUsage(),
		credentialVerification: params.CredentialVerification,
	}
	state.fork = newForkSession(state)

	return domain.Session{
		ID:       state.sessionID,
		AgentPID: "",
		Internal: state,
	}, nil
}

// RunTurn executes one pi turn on the session's
// [agentcore.ForkPerTurnSession] and relays normalized events via
// params.OnEvent.
func (a *PiAdapter) RunTurn(ctx context.Context, session domain.Session, params domain.RunTurnParams) (domain.TurnResult, error) {
	if params.OnEvent == nil {
		panic("pi: OnEvent must be non-nil")
	}

	state, ok := session.Internal.(*sessionState)
	if !ok {
		return domain.TurnResult{}, &domain.AgentError{
			Kind:    domain.ErrResponseError,
			Message: fmt.Sprintf("unexpected session internal type %T", session.Internal),
		}
	}

	state.resetTurn()

	return state.fork.RunTurn(ctx, params.Prompt, params.OnEvent)
}

// resetTurn clears the per-turn state before a turn starts.
func (s *sessionState) resetTurn() {
	s.work = agentcore.NewWorkObserver(agentcore.WorkSignals{AssistantOutput: true, ToolActivity: true})
	s.tools = agentcore.NewToolTracker()
	s.turnUsage = domain.TokenUsage{}
	s.turnByModel = nil
	s.turnModel = ""
	s.turnMeasured = false
	s.sawMessageEnd = false
	s.lastStopReason = ""
	s.lastErrorMessage = ""
	s.agentEnded = false
}

// StopSession terminates any active subprocess.
func (a *PiAdapter) StopSession(ctx context.Context, session domain.Session) error {
	state, ok := session.Internal.(*sessionState)
	if !ok {
		return &domain.AgentError{
			Kind:    domain.ErrResponseError,
			Message: fmt.Sprintf("unexpected session internal type %T", session.Internal),
		}
	}
	return state.fork.Stop(ctx)
}

// newForkSession builds the skeleton that runs state's turns.
func newForkSession(state *sessionState) *agentcore.ForkPerTurnSession {
	hooks := agentcore.ForkPerTurnHooks{
		BuildArgs: func(_ int, _ string) []string {
			return buildRunArgs(state, state.passthrough)
		},
		ParseLine: state.parseLine,
		GetUsage: func() (domain.TokenUsage, map[string]domain.TokenUsage, bool) {
			return state.usage.Snapshot(), state.usage.ByModel(), state.usage.Measured()
		},
		GetSessionID:      state.currentSessionID,
		OnFinalize:        state.finalizeTurn,
		PromptOnStdin:     true,
		FirstEventTimeout: agentcore.ReadTimeout(state.agentConfig),
		SettleUsage:       state.settleUsage,
	}
	return agentcore.NewForkPerTurnSession(&state.target, hooks, state.baseLogger, state.agentConfig.StopGraceMS)
}

// parseLine handles one stdout line as the skeleton's ParseLine hook. It
// returns a non-nil result for the agent_end that ends the run.
func (s *sessionState) parseLine(line []byte, emit func(domain.AgentEvent), _ string, spend *agentcore.TurnSpend) (any, error) {
	event, err := parseRunEvent(line)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	malformed := func(reason error) {
		emit(domain.AgentEvent{Type: domain.EventMalformed, Timestamp: now, Message: reason.Error()})
	}

	switch event.Type {
	case "session":
		id, cwd, err := event.sessionHeader()
		if err != nil {
			malformed(err)
			return nil, nil
		}
		if problem := s.checkSessionHeader(id, cwd); problem != "" {
			return nil, agentcore.EndTurn(domain.ErrResponseError, problem)
		}
		if s.applySessionEvent(id) {
			emit(domain.AgentEvent{
				Type:      domain.EventSessionStarted,
				Timestamp: now,
				SessionID: s.currentSessionID(),
				Message:   "session started",
			})
		}

	case "turn_start":
		s.sawMessageEnd = false
		agentcore.EmitNotification(emit, "")

	case "message_update":
		delta, err := event.assistantDelta()
		if err != nil {
			malformed(err)
			return nil, nil
		}
		switch delta.Type {
		case "text_start", "text_delta", "thinking_start", "thinking_delta":
			s.work.ObserveAssistantOutput()
			spend.MarkModelReached()
		case "toolcall_start", "toolcall_end":
			s.work.ObserveToolActivity()
			spend.MarkModelReached()
		}
		agentcore.EmitNotification(emit, "")

	case "message_end":
		msg, err := event.completedMessage()
		if err != nil {
			malformed(err)
			return nil, nil
		}
		if msg.Role == "assistant" {
			s.sawMessageEnd = true
			s.recordResponse(msg, emit, spend)
		}

	case "turn_end":
		msg, err := event.completedMessage()
		if err != nil {
			malformed(err)
			return nil, nil
		}
		if msg.Role == "assistant" && !s.sawMessageEnd {
			s.recordResponse(msg, emit, spend)
		}

	case "tool_execution_start":
		if event.ToolCallID != "" {
			s.tools.Begin(event.ToolCallID, agentcore.MapToolCall(toolMappings, event.ToolName, event.Args))
		}

	case "tool_execution_update", "compaction_start", "compaction_end", "auto_retry_start", "auto_retry_end":
		agentcore.EmitNotification(emit, "")

	case "tool_execution_end":
		if event.ToolName == "" {
			malformed(fmt.Errorf("tool_execution_end carries no toolName"))
			return nil, nil
		}
		call, durationMS, _ := s.tools.End(event.ToolCallID)
		s.work.ObserveToolActivity()
		spend.MarkModelReached()
		if event.DurationMS > 0 {
			durationMS = event.DurationMS
		}
		var message string
		if event.IsError {
			message = redact.Truncate(event.resultText(), 500)
		}
		emit(domain.AgentEvent{
			Type:           domain.EventToolResult,
			Timestamp:      now,
			ToolName:       event.ToolName,
			ToolKind:       call.Kind,
			ToolArgument:   call.Argument,
			ToolDurationMS: durationMS,
			ToolError:      event.IsError,
			Message:        message,
		})

	case "agent_end":
		if !event.WillRetry {
			s.agentEnded = true
			return &event, nil
		}

	default:
		if !event.known() {
			malformed(fmt.Errorf("unknown pi event type %q", event.Type))
		}
	}

	return nil, nil
}

// recordResponse takes in one completed assistant response: its text, its
// stop reason, and its usage.
func (s *sessionState) recordResponse(msg rawMessage, emit func(domain.AgentEvent), spend *agentcore.TurnSpend) {
	spend.MarkModelReached()
	s.lastStopReason = msg.StopReason
	s.lastErrorMessage = msg.ErrorMessage
	if text := msg.text(); text != "" {
		s.work.ObserveAssistantOutput()
		agentcore.EmitNotification(emit, redact.Truncate(text, 500))
	}
	if msg.Usage == nil {
		return
	}
	usage := msg.Usage.tokenUsage()
	name := msg.modelName()
	s.turnMeasured = true
	s.turnUsage = agentcore.AddUsage(s.turnUsage, usage)
	s.turnByModel = agentcore.AddUsageByModel(s.turnByModel, map[string]domain.TokenUsage{name: usage})
	s.turnModel = name
}

// settleUsage adds the turn's responses to the session total and reports
// the total as the turn's one usage event, on every ending of a started
// turn, so usage a cancelled or failed turn already measured is kept.
func (s *sessionState) settleUsage(_ context.Context, emit func(domain.AgentEvent), spend *agentcore.TurnSpend) {
	if s.turnMeasured {
		s.total = agentcore.AddUsage(s.total, s.turnUsage)
		s.totalByModel = agentcore.AddUsageByModel(s.totalByModel, s.turnByModel)
		s.usage.Settle(emit, &agentcore.RecoveredUsage{Run: s.total, ByModel: s.totalByModel, Model: s.turnModel})
	}
	if s.agentEnded {
		spend.MarkSettled()
	}
}

// finalizeTurn builds the disposition of a turn whose process exited. pi
// exits 0 for a response that stopped in failure, so the last response's
// stop reason is the only evidence of how the run ended.
func (s *sessionState) finalizeTurn(emit func(domain.AgentEvent), lastParsed any, exitCode int, waitErr error, _ []string, earlyExit *domain.AgentError, spend *agentcore.TurnSpend) (domain.TurnResult, *domain.AgentError) {
	ev := agentcore.TurnEvidence{
		ExitObserved: true,
		ExitCode:     exitCode,
		WaitErr:      waitErr,
		EarlyExit:    earlyExit,
	}
	ev.Work, ev.WorkDetail = s.work.Report()

	if lastParsed != nil {
		s.applyStopReason(&ev)
	}

	return s.usage.Finalize(emit, s.logger(), ev, s.currentSessionID(), 0, nil, *spend)
}

// applyStopReason sets ev's terminal report from the last response's stop
// reason. A run that ended without any assistant response leaves it unset.
func (s *sessionState) applyStopReason(ev *agentcore.TurnEvidence) {
	switch s.lastStopReason {
	case "":
	case "stop":
		ev.Terminal = agentcore.TerminalSuccess
	case "length":
		ev.Terminal = agentcore.TerminalFailure
		ev.TerminalErrorKind = domain.ErrTurnTokenLimit
		ev.TerminalMessage = "pi stopped the response at the output token limit"
	case "error", "aborted":
		ev.Terminal = agentcore.TerminalFailure
		ev.TerminalMessage = s.lastErrorMessage
		if ev.TerminalMessage == "" {
			ev.TerminalMessage = "pi reported stop reason " + s.lastStopReason
		}
	default:
		ev.Terminal = agentcore.TerminalFailure
		ev.TerminalErrorKind = domain.ErrTurnOutcomeUnknown
		ev.TerminalMessage = redact.Truncate(fmt.Sprintf("pi reported an unrecognized stop reason: %q", s.lastStopReason), 500)
	}
}

// checkSessionHeader validates a session header against the session's own
// identity and workspace, and returns the problem it found or "". The
// workspace is compared only for a local launch, where the header's cwd and
// the workspace are compared as the same directory rather than as strings.
// A remote launch's paths name the remote host's file system, which Sortie
// cannot inspect, so only the session id is checked.
func (s *sessionState) checkSessionHeader(id, cwd string) string {
	if s.target.RemoteCommand == "" && !sameDir(s.target.WorkspacePath, cwd) {
		return fmt.Sprintf("session %q runs in %q, want the turn's workspace %q", id, cwd, s.target.WorkspacePath)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID != "" && s.sessionID != id {
		return fmt.Sprintf("session id mismatch: expected %q, got %q", s.sessionID, id)
	}
	return ""
}

func sameDir(a, b string) bool {
	infoA, err := os.Stat(a)
	if err != nil {
		return false
	}
	infoB, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(infoA, infoB)
}

func (s *sessionState) logger() *slog.Logger {
	sessionID := s.currentSessionID()
	if sessionID == "" {
		return s.baseLogger
	}
	return logging.WithSession(s.baseLogger, sessionID)
}

func (s *sessionState) currentSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// applySessionEvent adopts the header's session id, and reports whether
// this is the first header of the Sortie session, so session_started is
// emitted exactly once even across a resume.
func (s *sessionState) applySessionEvent(eventSessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sessionID == "" {
		s.sessionID = eventSessionID
	}
	if s.sessionOpened {
		return false
	}
	s.sessionOpened = true
	return true
}
