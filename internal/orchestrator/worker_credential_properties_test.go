package orchestrator

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/prompt"
	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/waittest"
)

type credentialPropertyAdapter struct {
	verifyRunFn  func(ctx context.Context, params domain.RunTurnParams) (domain.TurnResult, error)
	workRunFn    func(params domain.RunTurnParams) (domain.TurnResult, error)
	workStartErr error

	mu                  sync.Mutex
	workingStartCalls   int
	workingRunCalls     int
	workingStartParams  domain.StartSessionParams
	verifyWorkspacePath string
}

type credentialPropertySessionMeta struct{}

var _ domain.AgentAdapter = (*credentialPropertyAdapter)(nil)

func (a *credentialPropertyAdapter) StartSession(_ context.Context, params domain.StartSessionParams) (domain.Session, error) {
	if params.CredentialVerification {
		a.mu.Lock()
		a.verifyWorkspacePath = params.WorkspacePath
		a.mu.Unlock()
		return domain.Session{ID: "sess-verify", Internal: &credentialPropertySessionMeta{}}, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workingStartCalls++
	a.workingStartParams = params
	if a.workStartErr != nil {
		return domain.Session{}, a.workStartErr
	}
	return domain.Session{ID: "sess-work"}, nil
}

func (a *credentialPropertyAdapter) RunTurn(ctx context.Context, session domain.Session, params domain.RunTurnParams) (domain.TurnResult, error) {
	if _, verification := session.Internal.(*credentialPropertySessionMeta); verification {
		if a.verifyRunFn != nil {
			return a.verifyRunFn(ctx, params)
		}
		return domain.TurnResult{SessionID: session.ID, ExitReason: domain.EventTurnCompleted}, nil
	}
	a.mu.Lock()
	a.workingRunCalls++
	a.mu.Unlock()
	if a.workRunFn != nil {
		return a.workRunFn(params)
	}
	return domain.TurnResult{SessionID: session.ID, ExitReason: domain.EventTurnCompleted}, nil
}

func (a *credentialPropertyAdapter) StopSession(context.Context, domain.Session) error { return nil }

func (a *credentialPropertyAdapter) working() (start, run int, params domain.StartSessionParams) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workingStartCalls, a.workingRunCalls, a.workingStartParams
}

func (a *credentialPropertyAdapter) workspacePath() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.verifyWorkspacePath
}

func runCredentialWorker(t *testing.T, ctx context.Context, cfg config.ServiceConfig, adapter domain.AgentAdapter, onEvent func(domain.AgentEvent), opts ...func(*WorkerDeps)) WorkerResult {
	t.Helper()
	cfg.Agent.MaxTurns = 1
	ec := newExitCapture()
	deps := WorkerDeps{
		TrackerAdapter:         &mockTrackerAdapter{},
		AgentAdapter:           adapter,
		ConfigFunc:             func() config.ServiceConfig { return cfg },
		PromptTemplateByIDFunc: func(string) *prompt.Template { return mustParseTemplate(t, "do work on {{ .issue.title }}") },
		ResumeSessionID:        "resume-abc",
		OnEvent: func(_ string, e domain.AgentEvent) {
			if onEvent != nil {
				onEvent(e)
			}
		},
		OnExit: ec.onExit,
		Logger: discardLogger(),
	}
	for _, opt := range opts {
		opt(&deps)
	}
	RunWorkerAttempt(ctx, workerTestIssue(), nil, deps)
	return ec.waitResult(t)
}

func runCredentialWorkerWithSelfReview(t *testing.T, ctx context.Context, cfg config.ServiceConfig, adapter domain.AgentAdapter, onEvent func(domain.AgentEvent)) WorkerResult {
	t.Helper()
	cfg.Agent.MaxTurns = 1
	ec := newExitCapture()
	RunWorkerAttempt(ctx, workerTestIssue(), nil, WorkerDeps{
		TrackerAdapter:         &mockTrackerAdapter{},
		AgentAdapter:           adapter,
		ConfigFunc:             func() config.ServiceConfig { return cfg },
		PromptTemplateByIDFunc: func(string) *prompt.Template { return mustParseTemplate(t, "do work on {{ .issue.title }}") },
		ResumeSessionID:        "resume-abc",
		WorkflowPath:           "/fake/WORKFLOW.md",
		OnEvent: func(_ string, e domain.AgentEvent) {
			if onEvent != nil {
				onEvent(e)
			}
		},
		OnExit: ec.onExit,
		Logger: discardLogger(),
	})
	return ec.waitResult(t)
}

func TestRunWorkerAttempt_FailedCredentialStepReachesNoWorkingSession(t *testing.T) {
	t.Parallel()

	adapter := &credentialPropertyAdapter{
		verifyRunFn: func(context.Context, domain.RunTurnParams) (domain.TurnResult, error) {
			return domain.TurnResult{}, &domain.AgentError{Kind: domain.ErrTurnFailed, Message: "the model rejected the request"}
		},
	}

	result := runCredentialWorker(t, context.Background(), defaultWorkerConfig(t.TempDir()), adapter, nil)

	if result.ExitKind != WorkerExitError {
		t.Errorf("WorkerResult.ExitKind = %q, want %q", result.ExitKind, WorkerExitError)
	}
	if result.Error == nil || !strings.HasPrefix(result.Error.Error(), "agent session start: ") || !strings.Contains(result.Error.Error(), "credential_unverified") {
		t.Errorf("WorkerResult.Error = %v, want a credential_unverified error with the %q prefix a failed working StartSession reports", result.Error, "agent session start: ")
	}
	if start, run, _ := adapter.working(); start != 0 || run != 0 {
		t.Errorf("working StartSession/RunTurn calls = (%d, %d), want (0, 0)", start, run)
	}
}

func TestRunWorkerAttempt_PassingCredentialStepStartsTheWorkingSession(t *testing.T) {
	t.Parallel()

	adapter := &credentialPropertyAdapter{}

	result := runCredentialWorker(t, context.Background(), defaultWorkerConfig(t.TempDir()), adapter, nil)

	if result.ExitKind != WorkerExitNormal {
		t.Fatalf("WorkerResult.ExitKind = %q, want %q (error: %v)", result.ExitKind, WorkerExitNormal, result.Error)
	}
	start, run, params := adapter.working()
	if start != 1 || run != 1 {
		t.Errorf("working StartSession/RunTurn calls = (%d, %d), want (1, 1)", start, run)
	}
	if params.CredentialVerification || params.ResumeSessionID != "resume-abc" {
		t.Errorf("working StartSessionParams = {CredentialVerification: %v, ResumeSessionID: %q}, want {false, %q}", params.CredentialVerification, params.ResumeSessionID, "resume-abc")
	}
}

// TestRunWorkerAttempt_HandoffEvidenceBaselineCapturedBeforeCredentialVerification
// proves the baseline is read before credential verification touches the
// workspace: the verification turn below only turns the workspace into a
// Git repository once it runs, so a baseline captured afterward would find
// a repository where a baseline captured beforehand finds none.
func TestRunWorkerAttempt_HandoffEvidenceBaselineCapturedBeforeCredentialVerification(t *testing.T) {
	t.Parallel()

	adapter := &credentialPropertyAdapter{}
	adapter.verifyRunFn = func(context.Context, domain.RunTurnParams) (domain.TurnResult, error) {
		initGitRepo(t, adapter.workspacePath())
		return domain.TurnResult{ExitReason: domain.EventTurnCompleted}, nil
	}

	result := runCredentialWorker(t, context.Background(), defaultWorkerConfig(t.TempDir()), adapter, nil)

	if result.HandoffEvidenceBaselineError == nil {
		t.Error("WorkerResult.HandoffEvidenceBaselineError = nil, want a non-Git-workspace error: the baseline must be captured before credential verification can turn the workspace into a Git repository")
	}
}

func TestRunWorkerAttempt_CredentialStepBounds(t *testing.T) {
	t.Parallel()

	type turnFn = func(context.Context, domain.RunTurnParams) (domain.TurnResult, error)
	silent := func(entered chan<- struct{}) turnFn {
		var once sync.Once
		return func(ctx context.Context, _ domain.RunTurnParams) (domain.TurnResult, error) {
			once.Do(func() { close(entered) })
			<-ctx.Done()
			return domain.TurnResult{}, ctx.Err()
		}
	}
	chatty := func(chan<- struct{}) turnFn {
		return func(ctx context.Context, params domain.RunTurnParams) (domain.TurnResult, error) {
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return domain.TurnResult{}, ctx.Err()
				case <-ticker.C:
					params.OnEvent(domain.AgentEvent{Type: domain.EventNotification, Timestamp: time.Now().UTC(), Message: "still working"})
				}
			}
		}
	}

	tests := []struct {
		name          string
		turnTimeoutMS int
		// cancelOnEntry stands in for stall detection cancelling the
		// running entry's context once the verification turn runs.
		cancelOnEntry  bool
		run            func(entered chan<- struct{}) turnFn
		wantExit       WorkerExitKind
		wantUnverified bool
	}{
		{
			name:          "stall cancellation wins over the turn bound",
			turnTimeoutMS: int(waittest.HangGuard.Milliseconds()),
			cancelOnEntry: true,
			run:           silent,
			wantExit:      WorkerExitCancelled,
		},
		{
			name:           "turn bound ends a verification that keeps sending events",
			turnTimeoutMS:  100,
			run:            chatty,
			wantExit:       WorkerExitError,
			wantUnverified: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := defaultWorkerConfig(t.TempDir())
			cfg.Agent.TurnTimeoutMS = tt.turnTimeoutMS
			ctx, cancel := context.WithCancel(waittest.Context(t))
			defer cancel()
			entered := make(chan struct{})
			if tt.cancelOnEntry {
				go func() {
					select {
					case <-entered:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			adapter := &credentialPropertyAdapter{verifyRunFn: tt.run(entered)}

			result := runCredentialWorker(t, ctx, cfg, adapter, nil)

			if result.ExitKind != tt.wantExit {
				t.Fatalf("WorkerResult.ExitKind = %q, want %q (error: %v)", result.ExitKind, tt.wantExit, result.Error)
			}
			agentErr, isAgentErr := errors.AsType[*domain.AgentError](result.Error)
			gotUnverified := isAgentErr && agentErr.Kind == domain.ErrCredentialUnverified
			if gotUnverified != tt.wantUnverified {
				t.Errorf("WorkerResult.Error = %v, credential_unverified = %v, want %v", result.Error, gotUnverified, tt.wantUnverified)
			}
			wantBound := fmt.Sprintf("did not complete a credential verification request within %d ms", tt.turnTimeoutMS)
			if tt.wantUnverified && !strings.Contains(agentErr.Message, wantBound) {
				t.Errorf("AgentError.Message = %q, want it to contain %q", agentErr.Message, wantBound)
			}
			if start, run, _ := adapter.working(); start != 0 || run != 0 {
				t.Errorf("working StartSession/RunTurn calls = (%d, %d), want (0, 0)", start, run)
			}
		})
	}
}

func TestRunWorkerAttempt_RelayedVerificationEventsCarryOnlyTheVerificationShape(t *testing.T) {
	t.Parallel()

	adapter := &credentialPropertyAdapter{
		verifyRunFn: func(_ context.Context, params domain.RunTurnParams) (domain.TurnResult, error) {
			params.OnEvent(domain.AgentEvent{
				Type:           domain.EventTokenUsage,
				Timestamp:      time.Now().UTC(),
				SessionID:      "raw-session-id",
				AgentPID:       "raw-pid",
				Usage:          domain.TokenUsage{InputTokens: 80, OutputTokens: 20, TotalTokens: 100},
				Model:          "raw-model",
				Message:        "raw message",
				ToolName:       "raw-tool",
				ToolDurationMS: 999,
				UsageWindows:   []domain.UsageWindow{{Key: "five_hour", Label: "5-hour limit"}},
			})
			params.OnEvent(domain.AgentEvent{
				Type:      domain.EventNotification,
				Timestamp: time.Now().UTC(),
				SessionID: "raw-session-id-2",
				Message:   "some notification text",
				ToolName:  "raw-tool-2",
			})
			return domain.TurnResult{ExitReason: domain.EventTurnCompleted}, nil
		},
	}

	var received []domain.AgentEvent
	result := runCredentialWorker(t, context.Background(), defaultWorkerConfig(t.TempDir()), adapter, func(e domain.AgentEvent) {
		received = append(received, e)
	})
	if result.ExitKind != WorkerExitNormal {
		t.Fatalf("WorkerResult.ExitKind = %q, want %q (error: %v)", result.ExitKind, WorkerExitNormal, result.Error)
	}

	if len(received) != 3 {
		t.Fatalf("deps.OnEvent received %d events, want 3 (the verification start notification and the two relayed events): %+v", len(received), received)
	}
	for i, e := range received {
		if e.Type != domain.EventTokenUsage && e.Type != domain.EventNotification {
			t.Errorf("received[%d].Type = %q, want token_usage or notification", i, e.Type)
		}
		if e.Message != "verifying the agent credential" {
			t.Errorf("received[%d].Message = %q, want %q", i, e.Message, "verifying the agent credential")
		}
		if e.SessionID != "" || e.AgentPID != "" || e.ToolName != "" || e.ToolDurationMS != 0 || e.UsageWindows != nil {
			t.Errorf("received[%d] = %+v, want no SessionID, AgentPID, tool field or usage windows", i, e)
		}
	}
	if received[1].Usage.TotalTokens != 100 || received[1].Model != "raw-model" {
		t.Errorf("received[1] usage = (%d, %q), want the token_usage event's own (100, %q)", received[1].Usage.TotalTokens, received[1].Model, "raw-model")
	}
}

func TestRunWorkerAttempt_TokenColumnsEqualVerificationPlusWorking(t *testing.T) {
	t.Parallel()

	verifyUsage := domain.TokenUsage{InputTokens: 80, OutputTokens: 20, TotalTokens: 100}
	workUsage := domain.TokenUsage{InputTokens: 150, OutputTokens: 50, TotalTokens: 200}
	workByModel := map[string]domain.TokenUsage{
		"model-verify": {InputTokens: 100, OutputTokens: 10, TotalTokens: 110},
		"model-sub":    {InputTokens: 40, OutputTokens: 10, TotalTokens: 50},
	}
	reportUsage := func(params domain.RunTurnParams, model string, usage domain.TokenUsage, byModel map[string]domain.TokenUsage) (domain.TurnResult, error) {
		params.OnEvent(domain.AgentEvent{Type: domain.EventTokenUsage, Timestamp: time.Now().UTC(), Usage: usage, UsageByModel: byModel, Model: model})
		return domain.TurnResult{SessionID: "sess", ExitReason: domain.EventTurnCompleted, Usage: usage, UsageMeasured: true}, nil
	}
	adapter := &credentialPropertyAdapter{
		verifyRunFn: func(_ context.Context, params domain.RunTurnParams) (domain.TurnResult, error) {
			return reportUsage(params, "model-verify", verifyUsage, nil)
		},
		workRunFn: func(params domain.RunTurnParams) (domain.TurnResult, error) {
			return reportUsage(params, "model-work", workUsage, workByModel)
		},
	}

	result := runCredentialWorker(t, context.Background(), defaultWorkerConfig(t.TempDir()), adapter, nil)
	if result.ExitKind != WorkerExitNormal {
		t.Fatalf("WorkerResult.ExitKind = %q, want %q (error: %v)", result.ExitKind, WorkerExitNormal, result.Error)
	}

	want := domain.TokenUsage{
		InputTokens:  verifyUsage.InputTokens + workUsage.InputTokens,
		OutputTokens: verifyUsage.OutputTokens + workUsage.OutputTokens,
		TotalTokens:  verifyUsage.TotalTokens + workUsage.TotalTokens,
	}
	got := domain.TokenUsage{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens, TotalTokens: result.Usage.TotalTokens}
	if got != want {
		t.Errorf("WorkerResult.Usage = %+v, want %+v", got, want)
	}
	if result.APIRequestCount < 2 || !result.UsageMeasured {
		t.Errorf("WorkerResult = {APIRequestCount: %d, UsageMeasured: %v}, want at least 2 requests counted and usage measured", result.APIRequestCount, result.UsageMeasured)
	}

	wantByModel := map[string]domain.TokenUsage{
		"model-verify": {InputTokens: 180, OutputTokens: 30, TotalTokens: 210},
		"model-sub":    {InputTokens: 40, OutputTokens: 10, TotalTokens: 50},
		"model-work":   {InputTokens: 10, OutputTokens: 30, TotalTokens: 40},
	}
	if !maps.Equal(result.UsageByModel, wantByModel) {
		t.Errorf("WorkerResult.UsageByModel = %+v, want %+v", result.UsageByModel, wantByModel)
	}
}

func TestRunWorkerAttempt_SelfReviewUsageIncludesVerificationOffset(t *testing.T) {
	t.Parallel()

	verifyUsage := domain.TokenUsage{InputTokens: 80, OutputTokens: 20, TotalTokens: 100, CacheReadTokens: 5}
	reviewUsage := domain.TokenUsage{InputTokens: 40, OutputTokens: 10, TotalTokens: 50, CacheReadTokens: 2}
	want := domain.TokenUsage{
		InputTokens:     verifyUsage.InputTokens + reviewUsage.InputTokens,
		OutputTokens:    verifyUsage.OutputTokens + reviewUsage.OutputTokens,
		TotalTokens:     verifyUsage.TotalTokens + reviewUsage.TotalTokens,
		CacheReadTokens: verifyUsage.CacheReadTokens + reviewUsage.CacheReadTokens,
	}

	adapter := &credentialPropertyAdapter{
		verifyRunFn: func(_ context.Context, params domain.RunTurnParams) (domain.TurnResult, error) {
			params.OnEvent(domain.AgentEvent{Type: domain.EventTokenUsage, Timestamp: time.Now().UTC(), Usage: verifyUsage})
			return domain.TurnResult{ExitReason: domain.EventTurnCompleted, Usage: verifyUsage, UsageMeasured: true}, nil
		},
	}
	var midReviewState workerState
	adapter.workRunFn = func(params domain.RunTurnParams) (domain.TurnResult, error) {
		if !isSelfReviewTurnPrompt(params.Prompt) {
			return domain.TurnResult{ExitReason: domain.EventTurnCompleted}, nil
		}
		params.OnEvent(domain.AgentEvent{Type: domain.EventTokenUsage, Timestamp: time.Now().UTC(), Usage: reviewUsage})
		_, _, startParams := adapter.working()
		midReviewState = readWorkerStateFile(t, startParams.WorkspacePath)
		writeVerdictFile(t, startParams.WorkspacePath, domain.ReviewVerdict{Verdict: "pass", Summary: "looks good"})
		return domain.TurnResult{ExitReason: domain.EventTurnCompleted, Usage: reviewUsage}, nil
	}

	cfg := defaultWorkerConfig(t.TempDir())
	cfg.SelfReview = config.SelfReviewConfig{Enabled: true, MaxIterations: 1}

	result := runCredentialWorkerWithSelfReview(t, context.Background(), cfg, adapter, nil)

	if result.ExitKind != WorkerExitNormal {
		t.Fatalf("ExitKind = %q, want %q (error: %v)", result.ExitKind, WorkerExitNormal, result.Error)
	}

	assertTokenUsageMatches(t, midReviewState, want)

	finalState := readWorkerStateFile(t, result.WorkspacePath)
	assertTokenUsageMatches(t, finalState, want)

	if result.Usage != want {
		t.Errorf("WorkerResult.Usage = %+v, want %+v", result.Usage, want)
	}
}

func TestRunWorkerAttempt_VerificationSessionIdentifierNeverAccepted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		workStartErr  error
		wantExitKind  WorkerExitKind
		wantSessionID string
	}{
		{
			name:          "normal exit",
			wantExitKind:  WorkerExitNormal,
			wantSessionID: "sess-work",
		},
		{
			name:          "verification precedes a failed working StartSession",
			workStartErr:  errors.New("boom"),
			wantExitKind:  WorkerExitError,
			wantSessionID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := defaultWorkerConfig(t.TempDir())
			cfg.Agent.MaxTurns = 1

			adapter := &credentialPropertyAdapter{
				workStartErr: tt.workStartErr,
				verifyRunFn: func(_ context.Context, params domain.RunTurnParams) (domain.TurnResult, error) {
					params.OnEvent(domain.AgentEvent{Type: domain.EventSessionStarted, SessionID: "V", Timestamp: time.Now().UTC()})
					return domain.TurnResult{SessionID: "V", ExitReason: domain.EventTurnCompleted}, nil
				},
			}

			ec := newExitCapture()
			RunWorkerAttempt(context.Background(), workerTestIssue(), nil, WorkerDeps{
				TrackerAdapter:         &mockTrackerAdapter{},
				AgentAdapter:           adapter,
				ConfigFunc:             func() config.ServiceConfig { return cfg },
				PromptTemplateByIDFunc: func(string) *prompt.Template { return mustParseTemplate(t, "do work on {{ .issue.title }}") },
				OnEvent:                func(_ string, _ domain.AgentEvent) {},
				OnExit:                 ec.onExit,
				Logger:                 discardLogger(),
				WorkflowPath:           "/fake/WORKFLOW.md",
				DispatchID:             "D1",
			})
			result := ec.waitResult(t)

			if result.ExitKind != tt.wantExitKind {
				t.Fatalf("WorkerResult.ExitKind = %q, want %q (error: %v)", result.ExitKind, tt.wantExitKind, result.Error)
			}
			if result.SessionID != tt.wantSessionID {
				t.Errorf("WorkerResult.SessionID = %q, want %q", result.SessionID, tt.wantSessionID)
			}

			wsPath := adapter.workspacePath()
			if dispatchRecordExists(wsPath) {
				rec := readDispatchRecord(t, wsPath)
				if rec.SessionID == "V" {
					t.Errorf("dispatch identity record = %+v, want SessionID never %q", rec, "V")
				}
			}
		})
	}
}

type scriptedTurn struct {
	reason domain.AgentEventType
	err    error
	cancel bool
}

type cacheStep struct {
	after        int
	arrival      registry.UsageArrival
	verify       scriptedTurn
	work         []scriptedTurn
	wantVerifies bool
	wantProven   []int
	wantRequests int
}

func runCacheStep(t *testing.T, cache *verificationCache, root string, step cacheStep) (result WorkerResult, verified, announced bool) {
	t.Helper()
	work := step.work
	if len(work) == 0 {
		work = []scriptedTurn{{}}
	}
	cfg := defaultWorkerConfig(root)
	cfg.Agent.MaxTurns = len(work)
	ctx, cancel := context.WithCancel(waittest.Context(t))
	defer cancel()
	play := func(s scriptedTurn, onEvent func(domain.AgentEvent)) (domain.TurnResult, error) {
		if s.cancel {
			cancel()
		}
		usage := domain.TokenUsage{TotalTokens: 100}
		onEvent(domain.AgentEvent{Type: domain.EventTokenUsage, Timestamp: time.Now().UTC(), Usage: usage})
		return domain.TurnResult{ExitReason: cmp.Or(s.reason, domain.EventTurnCompleted), Usage: usage, UsageMeasured: true}, s.err
	}
	turn := 0
	adapter := &credentialPropertyAdapter{
		verifyRunFn: func(_ context.Context, params domain.RunTurnParams) (domain.TurnResult, error) {
			return play(step.verify, params.OnEvent)
		},
		workRunFn: func(params domain.RunTurnParams) (domain.TurnResult, error) {
			turn++
			return play(work[turn-1], params.OnEvent)
		},
	}

	result = runCredentialWorker(t, ctx, cfg, adapter, func(e domain.AgentEvent) {
		announced = announced || e.Message == "verifying the agent credential"
	}, func(d *WorkerDeps) {
		d.VerificationCache, d.UsageArrival = cache, step.arrival
		d.ConfigFunc = func() config.ServiceConfig { return cfg }
	})

	return result, adapter.workspacePath() != "", announced
}

func provenMinutes(cache *verificationCache, start time.Time) []int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	var minutes []int
	for _, at := range cache.proven {
		minutes = append(minutes, int(at.Sub(start)/time.Minute))
	}
	slices.Sort(minutes)
	return minutes
}

func TestRunWorkerAttempt_VerificationCacheAcrossRuns(t *testing.T) {
	t.Parallel()

	turnFailed := &domain.AgentError{Kind: domain.ErrTurnFailed}
	refused := &domain.AgentError{Kind: domain.ErrCredentialUnverified}
	first := cacheStep{wantVerifies: true, wantProven: []int{0}, wantRequests: 2}
	none := registry.UsageArrivalNone

	tests := []struct {
		name  string
		steps []cacheStep
	}{
		{"skipped run counts no verification usage and relays no verification event", []cacheStep{first, {after: 14, wantProven: []int{14}, wantRequests: 1}}},
		{"skipped run under arrival none reports no measurement", []cacheStep{{arrival: none, wantVerifies: true, wantProven: []int{0}}, {after: 1, arrival: none, wantProven: []int{1}}}},
		{"successful working turn moves the window", []cacheStep{first, {after: 10, wantProven: []int{10}}, {after: 10, wantProven: []int{20}}}},
		{"no-error turn with another ending evicts without moving the window", []cacheStep{first,
			{after: 10, work: []scriptedTurn{{reason: domain.EventTurnFailed}}},
			{after: 1, wantVerifies: true, wantProven: []int{11}}}},
		{"error after a successful turn keeps the proof", []cacheStep{first,
			{after: 10, work: []scriptedTurn{{}, {err: turnFailed}}, wantProven: []int{10}},
			{after: 1, wantProven: []int{11}}}},
		{"refused credential after a successful turn evicts", []cacheStep{first,
			{after: 10, work: []scriptedTurn{{}, {err: refused}}},
			{after: 1, wantVerifies: true, wantProven: []int{11}}}},
		{"failed verification evicts the stale proof", []cacheStep{first, {after: 16, verify: scriptedTurn{err: turnFailed}, wantVerifies: true}}},
		{"verification cancelled mid-flight keeps the stale proof", []cacheStep{first,
			{after: 16, verify: scriptedTurn{cancel: true, err: context.Canceled}, wantVerifies: true, wantProven: []int{0}}}},
		{"working turn cancelled mid-flight neither refreshes nor evicts", []cacheStep{first, {after: 10, work: []scriptedTurn{{cancel: true}}, wantProven: []int{0}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				cache := newVerificationCache()
				root := t.TempDir()
				start := time.Now()

				for i, step := range tt.steps {
					time.Sleep(time.Duration(step.after) * time.Minute)

					result, verified, announced := runCacheStep(t, cache, root, step)

					if got := provenMinutes(cache, start); verified != step.wantVerifies || announced != step.wantVerifies || !slices.Equal(got, step.wantProven) {
						t.Errorf("run %d = {verified: %v, announced: %v, proofs at minutes %v}, want {%v, %v, %v}", i+1, verified, announced, got, step.wantVerifies, step.wantVerifies, step.wantProven)
					}
					countsOff := step.wantRequests > 0 && (result.APIRequestCount != step.wantRequests || int(result.Usage.TotalTokens) != step.wantRequests*100)
					if countsOff || (step.arrival == registry.UsageArrivalNone && result.UsageMeasured) {
						t.Errorf("run %d = {APIRequestCount: %d, Usage.TotalTokens: %d, UsageMeasured: %v}, want {%d requests of 100 tokens, none under arrival none}", i+1, result.APIRequestCount, result.Usage.TotalTokens, result.UsageMeasured, step.wantRequests)
					}
				}
			})
		})
	}
}
