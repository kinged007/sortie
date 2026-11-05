// Package pi implements [domain.AgentAdapter] for the Pi CLI (pi 0.99.0
// or later). It runs one `pi -p --mode json` subprocess per Sortie turn
// on the shared fork-per-turn skeleton, normalizes the JSON event stream
// into domain events, and settles the session's cumulative token usage
// from the usage figure each pi assistant response carries.
//
// pi loads MCP servers from mcp.json and from extensions. The adapter
// writes the rendered tool-server document to a derived file in the
// workspace and loads a static bridge extension through --extension that
// registers its servers with pi.registerMcpServer, so a local session can
// call Sortie's tools.
package pi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/agent/sshutil"
	"github.com/sortie-ai/sortie/internal/agent/usage"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/logging"
	"github.com/sortie-ai/sortie/internal/redact"
	"github.com/sortie-ai/sortie/internal/registry"
)

const defaultCommand = "pi"

// usageSources: each assistant message in the output carries one model
// request's token counts and names the model that served it. The adapter
// sums them over the turn and reports the figure when the turn ends. The
// runtime keeps no records the adapter reads.
var usageSources = usage.Sources{
	Live: usage.Source{
		Category: usage.SourceOutput,
		Strategy: usage.StrategyLive,
		Spend: usage.SpendDeclaration{
			Shape:             usage.SpendPerRequest,
			ReasoningInOutput: true,
			ServedModel:       true,
		},
	},
}

func init() {
	registry.Agents.RegisterWithMeta("pi", NewPiAdapter, registry.AgentMeta{
		RequiresCommand:               true,
		DefaultCommand:                defaultCommand,
		ValidateAgentConfig:           validateConfig,
		MCPDialect:                    registry.MCPDialectMCPServersJSON,
		UsageSources:                  usageSources,
		UsageLimitRecognition:         registry.UsageLimitNotRecognized,
		UsageLimitNotRecognizedReason: "pi reports a refused model response as free text in an assistant message and names no usage-limit marker",
		CredentialEnv:                 registry.DeclareCredentialEnv(),
		EffortForwarding:              registry.EffortForwarded,
		VerificationContext: registry.DeclareVerificationContext(
			registry.VerificationKeepsAllTools,
			registry.VerificationKeepsProjectInstructions,
			registry.VerificationKeepsUserInstructions,
			registry.VerificationKeepsSystemPrompt,
		),
	})
}

var _ domain.AgentAdapter = (*PiAdapter)(nil)

type PiAdapter struct{}

type sessionState struct {
	target      agentcore.LaunchTarget
	agentConfig domain.AgentConfig
	passthrough passthroughConfig
	baseLogger  *slog.Logger
	usage       *agentcore.TurnEndUsage
	fork        *agentcore.ForkPerTurnSession

	mu            sync.Mutex
	sessionID     string
	sessionOpened bool
	closed        bool

	// total is the session's run-cumulative token usage: the sum of
	// every model response every turn of this session produced. It is
	// only read and written on the goroutine executing the session's
	// single active turn, which the orchestrator never overlaps.
	total domain.TokenUsage

	// turn is the active turn's own state, reset by RunTurn.
	turn turnState

	// mcpBridgePath is the absolute path to the static bridge extension
	// pi loads through --extension when the session carries Sortie MCP
	// servers locally. Empty for a remote launch, an empty
	// MCPConfigPath, or a configuration declaring no server.
	mcpBridgePath string

	// mcpConfigPath is the derived configuration file the bridge extension
	// reads, named by SORTIE_PI_MCP_SERVERS. Empty when the session
	// carries no server.
	mcpConfigPath string

	// tools is the session's shared tool-server delivery.
	tools *agentcore.ToolSession

	credentialVerification bool
}

// turnState is what one turn's lines have shown so far.
type turnState struct {
	tools *agentcore.ToolTracker
	work  *agentcore.WorkObserver

	// usage is this turn's own model responses, summed. usageModel
	// names the model the last counted response came from, which is
	// what a turn carrying several models reports.
	usage      domain.TokenUsage
	usageModel string

	// assistantFailure holds the failure of the last assistant response, or ""
	// when it completed. pi retries a response that stopped with "error"
	// inside the same process, so only the last response decides the turn.
	// pi --mode json exits 0 for "error" and "aborted", so this is the
	// only evidence such a turn failed.
	assistantFailure string

	// sawAssistantMessageEnd records whether the current pi turn already
	// delivered its authoritative message_end, so turn_end does not
	// report the same assistant response's text a second time.
	sawAssistantMessageEnd bool
}

// NewPiAdapter creates an [PiAdapter]. Settings are resolved per
// session in [PiAdapter.StartSession] from
// [domain.StartSessionParams.Settings].
func NewPiAdapter() (domain.AgentAdapter, error) {
	return &PiAdapter{}, nil
}

// StartSession checks that the installed pi is a supported release,
// resolves the launch target, confirms or replaces the session to resume,
// prepares the tool-server bridge on a local launch, and initializes
// adapter-owned session state without starting a turn subprocess.
func (a *PiAdapter) StartSession(ctx context.Context, params domain.StartSessionParams) (domain.Session, error) {
	pt, faults := parsePassthroughConfig(params.Settings)
	if len(faults) > 0 {
		messages := make([]string, len(faults))
		for i, f := range faults {
			messages[i] = f.Message
		}
		firstErr := faults[0].Err
		if firstErr == nil {
			firstErr = errors.New(faults[0].Message)
		}
		return domain.Session{}, agentcore.SettingsError(strings.Join(messages, "; "), firstErr)
	}
	target, agentErr := agentcore.ResolveLaunchTarget(params, defaultCommand)
	if agentErr != nil {
		return domain.Session{}, agentErr
	}

	state := &sessionState{
		target:                 target,
		agentConfig:            params.AgentConfig,
		passthrough:            pt,
		baseLogger:             slog.Default().With(slog.String("component", "pi-adapter")),
		usage:                  agentcore.NewTurnEndUsage(),
		credentialVerification: params.CredentialVerification,
	}

	if versionErr := checkRuntimeVersion(ctx, state); versionErr != nil {
		return domain.Session{}, versionErr
	}

	start, startErr := agentcore.ResolveSessionStart(ctx, params.ResumeSessionID,
		agentcore.RuntimeFileProbe(target, agentcore.ReadTimeout(params.AgentConfig), sessionFile), state.baseLogger)
	if startErr != nil {
		return domain.Session{}, startErr
	}
	state.sessionID = start.ResumeID()

	tools, toolsErr := agentcore.ResolveToolDelivery(ctx, params, target, registry.MCPDialectMCPServersJSON, state.baseLogger)
	if toolsErr != nil {
		return domain.Session{}, toolsErr
	}
	state.tools = tools

	remote := target.RemoteCommand != ""
	if remote {
		tools.WithholdForRuntime()
	}

	var bridgePath, configPath string
	if !remote && tools.Configured() && !params.CredentialVerification {
		document := tools.Delivery(sshutil.ForwardOutcome{}).Document
		var err error
		if configPath, bridgePath, err = writeDerivedConfig(target.WorkspacePath, document); err != nil {
			_ = tools.Close()
			return domain.Session{}, &domain.AgentError{
				Kind:    domain.ErrResponseError,
				Message: fmt.Sprintf("prepare pi mcp bridge: %v", err),
				Err:     err,
			}
		}
	}
	state.mcpBridgePath = bridgePath
	state.mcpConfigPath = configPath
	state.fork = newForkSession(state)

	return domain.Session{
		ID:             state.sessionID,
		AgentPID:       "",
		Internal:       state,
		NoToolChannel:  tools.NoToolChannel(),
		CarriedHistory: start.CarriedHistory(),
	}, nil
}

// RunTurn executes one Pi turn on the session's
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

	state.mu.Lock()
	closed := state.closed
	state.mu.Unlock()
	if closed {
		return domain.TurnResult{}, &domain.AgentError{
			Kind:    domain.ErrResponseError,
			Message: "session already stopped",
		}
	}

	state.turn = turnState{
		tools: agentcore.NewToolTracker(),
		work:  agentcore.NewWorkObserver(agentcore.WorkSignals{AssistantOutput: true, ToolActivity: true}),
	}

	return state.fork.RunTurn(ctx, params.Prompt, params.OnEvent)
}

// StopSession terminates any active subprocess and closes the session's
// tool-server delivery.
func (a *PiAdapter) StopSession(ctx context.Context, session domain.Session) error {
	state, ok := session.Internal.(*sessionState)
	if !ok {
		return &domain.AgentError{
			Kind:    domain.ErrResponseError,
			Message: fmt.Sprintf("unexpected session internal type %T", session.Internal),
		}
	}

	state.mu.Lock()
	state.closed = true
	state.mu.Unlock()

	stopErr := state.fork.Stop(ctx)
	_ = state.tools.Close()
	return stopErr
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
		FirstEventTimeout: agentcore.FirstResponseTimeout(state.agentConfig),
		SettleUsage:       state.settleUsage,
		TurnEnv:           func() ([]string, []sshutil.EnvVar) { return buildTurnEnv(state), nil },
	}
	if state.target.RemoteCommand == "" {
		hooks.Tools = state.tools
	}
	return agentcore.NewForkPerTurnSession(&state.target, hooks, state.baseLogger, state.agentConfig.StopGraceMS)
}

// parseLine applies one stdout line to the turn. A line that is not a
// pi event object is rejected, so the skeleton reports it as malformed.
// It returns a non-nil result when the line carries an assistant failure.
func (s *sessionState) parseLine(line []byte, emit func(domain.AgentEvent), _ string, spend *agentcore.TurnSpend) (any, error) {
	rawEvent, err := parseRunEvent(line)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	malformed := func(reason error) {
		emit(domain.AgentEvent{
			Type:      domain.EventMalformed,
			Timestamp: now,
			Message:   reason.Error(),
		})
	}
	turn := &s.turn

	if rawEvent.Type == "session" {
		id, cwd, err := rawEvent.sessionHeader()
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
		return nil, nil
	}

	var failure any
	switch rawEvent.Type {
	case "turn_start":
		turn.sawAssistantMessageEnd = false

	case "message_update":
		delta, err := rawEvent.assistantDelta()
		if err != nil {
			malformed(err)
			return nil, nil
		}
		switch delta.Type {
		case "text_start", "text_delta", "thinking_start", "thinking_delta":
			turn.work.ObserveAssistantOutput()
			spend.MarkModelReached()
		case "toolcall_start", "toolcall_end":
			turn.work.ObserveToolActivity()
			spend.MarkModelReached()
		}

	case "message_end":
		msg, err := rawEvent.completedMessage()
		if err != nil {
			malformed(err)
			return nil, nil
		}
		if msg.Role != "assistant" {
			return nil, nil
		}
		turn.sawAssistantMessageEnd = true
		turn.assistantFailure = msg.failure()
		if text := msg.text(); text != "" {
			turn.work.ObserveAssistantOutput()
			spend.MarkModelReached()
			agentcore.EmitNotification(emit, redact.Truncate(text, 500))
		}
		if turn.assistantFailure != "" {
			failure = turn.assistantFailure
		}

	case "turn_end":
		msg, err := rawEvent.completedMessage()
		if err != nil {
			malformed(err)
			return nil, nil
		}
		// turn_end names the same assistant response the matching
		// message_end already reported, so it is the fallback for
		// the text and the only source of the response's usage.
		if msg.Role == "assistant" && !turn.sawAssistantMessageEnd {
			turn.assistantFailure = msg.failure()
			if text := msg.text(); text != "" {
				turn.work.ObserveAssistantOutput()
				spend.MarkModelReached()
				agentcore.EmitNotification(emit, redact.Truncate(text, 500))
			}
			if turn.assistantFailure != "" {
				failure = turn.assistantFailure
			}
		}
		if msg.Role == "assistant" && msg.Usage != nil {
			turn.usage = agentcore.AddUsage(turn.usage, msg.Usage.tokenUsage())
			if msg.Model != "" {
				turn.usageModel = msg.Model
			}
		}

	case "tool_execution_start":
		if rawEvent.ToolCallID != "" {
			turn.tools.Begin(rawEvent.ToolCallID, agentcore.ToolCall{Name: rawEvent.ToolName})
		}

	case "tool_execution_end":
		_, durationMS, _ := turn.tools.End(rawEvent.ToolCallID)
		if rawEvent.ToolName == "" {
			malformed(fmt.Errorf("tool_execution_end carries no toolName"))
			return nil, nil
		}
		turn.work.ObserveToolActivity()
		spend.MarkModelReached()
		if rawEvent.DurationMS > 0 {
			durationMS = rawEvent.DurationMS
		}
		var message string
		if rawEvent.IsError {
			message = redact.Truncate(rawEvent.resultText(), 500)
		}
		emit(domain.AgentEvent{
			Type:           domain.EventToolResult,
			Timestamp:      now,
			ToolName:       rawEvent.ToolName,
			ToolDurationMS: durationMS,
			ToolError:      rawEvent.IsError,
			Message:        message,
		})

	case "compaction_end":
		usage, ok, err := rawEvent.compactionUsage()
		if err != nil {
			malformed(err)
			return nil, nil
		}
		if ok {
			turn.usage = agentcore.AddUsage(turn.usage, usage)
		}
		if rawEvent.ErrorMessage != "" && !rawEvent.Aborted {
			agentcore.EmitNotification(emit, "pi compaction failed: "+rawEvent.ErrorMessage)
		}

	default:
		if !rawEvent.known() {
			malformed(fmt.Errorf("unknown pi event type %q", rawEvent.Type))
		}
	}

	return failure, nil
}

// settleUsage folds this turn's own model responses into the session's
// run-cumulative usage and reports the figure, when the turn produced one.
// Only a clean exit makes the figure cover the whole turn; a killed or
// cancelled turn holds the responses seen so far.
func (s *sessionState) settleUsage(_ context.Context, emit func(domain.AgentEvent), spend *agentcore.TurnSpend, exitedClean bool) {
	if s.turn.usage == (domain.TokenUsage{}) {
		return
	}
	spend.MarkModelReached()
	if exitedClean {
		spend.MarkSettled()
	}
	s.total = agentcore.AddUsage(s.total, s.turn.usage)
	s.usage.Settle(emit, &agentcore.RecoveredUsage{Run: s.total, Model: s.turn.usageModel})
}

// finalizeTurn builds and emits the turn's terminal disposition once the
// subprocess has exited on its own.
func (s *sessionState) finalizeTurn(emit func(domain.AgentEvent), _ any, exitCode int, waitErr error, stderrLines []string, earlyExit *domain.AgentError, spend *agentcore.TurnSpend) (domain.TurnResult, *domain.AgentError) {
	for _, line := range stderrLines {
		if text := strings.TrimSpace(agentcore.SanitizeLine(line)); strings.HasPrefix(text, bridgeFailurePrefix) {
			s.logger().Warn("pi could not load Sortie's MCP servers", slog.String("detail", redact.Truncate(text, 300)))
		}
	}

	ev := agentcore.TurnEvidence{
		ExitObserved: true,
		ExitCode:     exitCode,
		WaitErr:      waitErr,
		EarlyExit:    earlyExit,
	}
	ev.Work, ev.WorkDetail = s.turn.work.Report()

	if s.turn.assistantFailure != "" {
		// pi --mode json applies no exit-code check to a failed
		// response, so this is what keeps a 0-exit provider failure
		// from being recorded as a successful turn.
		ev.Terminal = agentcore.TerminalFailure
		ev.TerminalErrorKind = domain.ErrResponseError
		ev.TerminalMessage = s.turn.assistantFailure
		if excerpt := stderrExcerpt(stderrLines); excerpt != "" {
			ev.TerminalMessage += ": " + excerpt
		}
	}

	return s.usage.Finalize(emit, s.logger(), ev, s.currentSessionID(), 0, nil, *spend)
}

// checkSessionHeader validates a session header against the session's
// own identity and workspace, and returns the problem it found or "".
func (s *sessionState) checkSessionHeader(id, cwd string) string {
	if want := filepath.Clean(s.target.WorkspacePath); filepath.Clean(cwd) != want {
		return fmt.Sprintf("session %q runs in %q, want the turn's workspace %q", id, cwd, s.target.WorkspacePath)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID != "" && s.sessionID != id {
		return fmt.Sprintf("session id mismatch: expected %q, got %q", s.sessionID, id)
	}
	return ""
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

// stderrExcerptLines and stderrExcerptRunes bound how much of a failed
// turn's stderr reaches its terminal report.
const (
	stderrExcerptLines = 3
	stderrExcerptRunes = 300
)

// stderrExcerpt returns a bounded excerpt of the turn's stderr lines
// for its terminal failure message, or "" when the turn wrote none.
func stderrExcerpt(lines []string) string {
	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			kept = append(kept, trimmed)
		}
		if len(kept) == stderrExcerptLines {
			break
		}
	}
	return redact.Truncate(strings.Join(kept, "; "), stderrExcerptRunes)
}
