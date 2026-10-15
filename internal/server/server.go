// Package server implements Sortie's embedded HTTP server for
// observability and operational control. Start with [New] to construct
// a server and [Server.ListenAndServe] to begin accepting connections.
package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/orchestrator"
	"github.com/sortie-ai/sortie/internal/server/ui"
)

// SnapshotFunc returns a point-in-time capture of the orchestrator's
// runtime state. The server calls this on each API request; it must
// be safe to call from any goroutine.
type SnapshotFunc func() (orchestrator.RuntimeSnapshotResult, error)

// RefreshFunc signals the orchestrator to perform an immediate
// poll+reconciliation cycle. Returns true if the signal was accepted,
// false if it was coalesced (channel already has a pending signal).
type RefreshFunc func() bool

// RetryNowFunc makes the scheduled retry of the issue with the given
// identifier due now; ctx is the request's context.
type RetryNowFunc func(ctx context.Context, identifier string) orchestrator.RetryNowResult

// AcknowledgeFunc records that a person has seen the attention item with the
// given ID; ctx is the request's context.
type AcknowledgeFunc func(ctx context.Context, itemID string) orchestrator.AcknowledgeResult

// CheckNowFunc makes the next check of an issue's pending pull request
// reaction of the given kind due now; ctx is the request's context.
type CheckNowFunc func(ctx context.Context, identifier, kind string) orchestrator.CheckNowResult

// RunHistoryEntry is a server-layer view of a completed run attempt.
// Decoupled from persistence types to avoid leaking internal packages.
type RunHistoryEntry struct {
	// Identifier is the tracker-assigned issue identifier (e.g., "PROJ-123").
	Identifier string
	// DisplayID is the short display label that the JSON API reports.
	DisplayID string
	// Attempt is the one-based retry attempt number.
	Attempt int
	// Status is the terminal run outcome: "succeeded", "failed",
	// "cancelled", "ci_failed", "needs_person", or "budget_stopped".
	Status string
	// WorkflowFile is the path to the workflow definition used.
	WorkflowFile string
	// StartedAt is the formatted start timestamp.
	StartedAt string
	// CompletedAt is the formatted completion timestamp.
	CompletedAt string
	// Error holds the error message when Status indicates failure.
	// Nil when no error occurred.
	Error *string
	// TurnsCompleted is the number of agent turns completed before exit.
	TurnsCompleted int
	// RuleName is the dispatch rule that routed the run; empty when none did.
	RuleName string
	// ChainID is the stage chain the run belongs to; empty for a run
	// recorded before chains existed.
	ChainID string
	// StagePrevious is the rule whose hop led to the run; empty when none
	// did.
	StagePrevious string
	// StageTarget is the rule the run's hop decision targeted; empty when
	// the run reached none.
	StageTarget string
	// StageResult is the result of the run's hop decision; empty when
	// StageTarget is.
	StageResult string
	// ID is the run's identifier in run history; it grows with every
	// recorded run and is the paging key.
	ID int64
	// IssueID is the tracker-assigned issue ID.
	IssueID string
	// IssueTitle is the title as recorded, unmasked.
	IssueTitle string
	// IssueURL is the tracker link as recorded, unmasked.
	IssueURL string
	// AgentKind is the agent adapter kind that ran the session.
	AgentKind string
	// ConfiguredModel is the model the dispatch rule configured.
	ConfiguredModel string
	// ConfiguredEffort is the reasoning effort the dispatch rule configured.
	ConfiguredEffort string
	// ReportedModel is the model the agent reported using.
	ReportedModel string
	// DispatchedAt is the dispatch timestamp as stored; empty when unrecorded.
	DispatchedAt string
	// Tokens holds the run's token figures; meaningful only when
	// TokensMeasured is true.
	Tokens domain.TokenUsage
	// TokensMeasured is false when the agent reported no usage.
	TokensMeasured bool
	// UnaccountedTurns counts turns whose usage the agent never reported.
	UnaccountedTurns int
	// StagePreviousOutcome is the outcome of the run that led to this one.
	StagePreviousOutcome string
	// ReviewMetadata is the self-review JSON as recorded; nil when
	// self-review did not run.
	ReviewMetadata *string
	// OriginRecorded is false for a run recorded before the origin fields
	// existed; Trigger, ReactionKind, SessionID, and SSHHost are then unknown.
	OriginRecorded bool
	// Trigger is what started the retry that dispatched the run; empty for
	// none, nil when the run's retry never stored it.
	Trigger *string
	// ReactionKind is the dispatching retry's reaction kind; empty for none.
	ReactionKind string
	// SessionID is the agent session the run used; empty when it started none.
	SessionID string
	// SSHHost is the SSH worker the run executed on; empty for a local run.
	SSHHost string
}

// RunHistoryQuery selects completed runs; a zero field matches every run.
type RunHistoryQuery struct {
	IssueID, Identifier, Status, StageTarget string
	// RuleName nil matches every run; a pointer to "" selects the runs that
	// record no rule name.
	RuleName *string
	// Since keeps runs completed at or after it.
	Since *time.Time
	// Until keeps runs completed before it.
	Until *time.Time
	// EndedSince keeps runs of issues whose outcome was observed at or after it.
	EndedSince *time.Time
	// EndedUntil keeps runs of issues whose outcome was observed before it.
	EndedUntil *time.Time
	// BeforeID keeps runs recorded before this run ID; 0 starts at the newest.
	BeforeID int64
	// Limit is the most runs returned.
	Limit int
}

// IssueGroupQuery selects the issues of run history by issue; a zero field
// matches every run.
type IssueGroupQuery struct {
	// IssueKey keeps the issues with a run whose identifier or display
	// identifier equals it.
	IssueKey, Status, StageTarget string
	// RuleName nil matches every run; a pointer to "" selects the runs that
	// record no rule name.
	RuleName *string
	// BeforeID continues after the group whose newest matching run has this
	// ID; 0 starts at the newest group.
	BeforeID int64
	// Limit is the most groups returned.
	Limit int
}

// IssueGroupKey is one issue with a matching run and the ID of its newest
// matching run.
type IssueGroupKey struct {
	IssueID     string
	NewestRunID int64
}

// IssueOutcome is how an issue's latest work ended, as the orchestrator
// observed it. State is empty for a merged outcome; MergeReactionKind,
// PRNumber, and PRRepository are set only for a merged outcome.
type IssueOutcome struct {
	// Kind is "handed_off", "merged", or "terminal".
	Kind string
	// ObservedAt is when the outcome was observed, as stored.
	ObservedAt string
	// State is the tracker state a handoff wrote or a terminal read found,
	// unmasked.
	State string
	// MergeReactionKind is the reaction kind that observed the merge.
	MergeReactionKind string
	// PRNumber is the merged pull request number.
	PRNumber int
	// PRRepository is the merged pull request's owner/repo, unmasked.
	PRRepository string
}

// IssueRunTotals is one issue's spend over all of its runs.
type IssueRunTotals struct {
	// Usage counts runs and prices spend per agent kind.
	Usage orchestrator.SnapshotIssueUsage
	// Tokens sums each token class over the measured runs.
	Tokens domain.TokenUsage
}

// RunHistoryReader reads persisted run history. Runs come newest first, and
// an implementation is safe for concurrent use.
type RunHistoryReader interface {
	QueryRuns(ctx context.Context, q RunHistoryQuery) ([]RunHistoryEntry, error)
	// QueryIssueGroups returns the issues whose runs match q, ordered by their
	// newest matching run, newest first.
	QueryIssueGroups(ctx context.Context, q IssueGroupQuery) ([]IssueGroupKey, error)
	IssueTotals(ctx context.Context, issueID string) (IssueRunTotals, error)
	// IssueOutcomes returns the outcome of each listed issue that has one,
	// keyed by issue ID.
	IssueOutcomes(ctx context.Context, issueIDs []string) (map[string]IssueOutcome, error)
}

// DBPingFunc checks whether the SQLite database is accessible.
// Returns nil on success, an error on failure. Called on each
// GET /readyz request.
type DBPingFunc func(ctx context.Context) error

// Params holds construction-time dependencies for [New].
type Params struct {
	// SnapshotFn returns the current runtime state. Called on each
	// GET /api/v1/state and GET /api/v1/{identifier}.
	SnapshotFn SnapshotFunc

	// WorkflowShapeFn returns the effective workflow shape. Called on
	// each GET /api/v1/state; nil omits config.
	WorkflowShapeFn func() orchestrator.WorkflowShape

	// HealthFn returns the health of Sortie's own checks. Called on each
	// GET /api/v1/state; nil omits health.
	HealthFn func() orchestrator.SnapshotHealth

	// RefreshFn signals an immediate poll+reconciliation cycle.
	// Called on POST /api/v1/refresh.
	RefreshFn RefreshFunc

	// RetryNowFn makes an issue's scheduled retry due now. Called on
	// POST /api/v1/retries/{identifier}/run; nil answers 503.
	RetryNowFn RetryNowFunc

	// AcknowledgeFn records that a person has seen an attention item. Called
	// on POST /api/v1/attention/{id}/acknowledge; nil answers 503.
	AcknowledgeFn AcknowledgeFunc

	// CheckNowFn makes the next check of a pending pull request reaction due
	// now. Called on POST /api/v1/reactions/{identifier}/{kind}/check; nil
	// answers 503.
	CheckNowFn CheckNowFunc

	// Logger is the structured logger for request and error logging.
	Logger *slog.Logger

	// Addr is the TCP address to listen on (e.g. "127.0.0.1:8080").
	Addr string

	// Version is the build version string that the JSON API reports.
	Version string

	// StartedAt is the time the process started, used to compute
	// uptime in the JSON API.
	StartedAt time.Time

	// MetricsRegistry is an optional Prometheus registry for the
	// /metrics endpoint. When non-nil, New registers a handler at
	// GET /metrics that serves the Prometheus text exposition format.
	// Obtain via [PromMetrics.Registry]. When nil, /metrics is
	// not registered.
	MetricsRegistry *prometheus.Registry

	// DBPingFn checks database accessibility for /readyz.
	// When nil, the database check always reports "pass".
	DBPingFn DBPingFunc

	// PreflightFn returns the result of the most recent dispatch
	// preflight validation. Called on each GET /readyz request.
	// When nil, the preflight check always reports "pass".
	PreflightFn func() bool

	// WorkflowLoadedFn returns whether the workflow file has been
	// successfully loaded at least once. Called on each GET /readyz.
	// When nil, the workflow check always reports "pass".
	WorkflowLoadedFn func() bool

	// RunHistory reads persisted run history. When nil, GET /api/v1/runs
	// answers 503, GET /api/v1/{identifier} serves live detail with a null
	// history.
	RunHistory RunHistoryReader

	// TokenRates maps agent adapter kind strings to per-token USD
	// rates for cost estimation in the JSON API. When nil or empty,
	// the API reports raw token counts without cost estimates.
	TokenRates TokenRates

	// Stats computes the run statistics report. Called on each
	// GET /api/v1/stats; nil answers 503.
	Stats StatsFunc
}

// Server is the embedded HTTP server for the JSON API, the dashboard
// application's assets, and metrics endpoints. Construct via [New]. Safe for concurrent use
// after construction.
type Server struct {
	httpServer      *http.Server
	mux             *http.ServeMux
	logger          *slog.Logger
	snapshotFn      SnapshotFunc
	refreshFn       RefreshFunc
	retryNowFn      RetryNowFunc
	acknowledgeFn   AcknowledgeFunc
	checkNowFn      CheckNowFunc
	workflowShapeFn func() orchestrator.WorkflowShape
	healthFn        func() orchestrator.SnapshotHealth
	version         string
	startedAt       time.Time

	drainingFlag     atomic.Bool
	dbPingFn         DBPingFunc
	preflightFn      func() bool
	workflowLoadedFn func() bool
	runHistory       RunHistoryReader
	tokenRates       TokenRates
	stats            StatsFunc

	changes *changeFeed
	timing  streamTiming
}

// Compile-time assertion: Server satisfies orchestrator.Observer.
var _ orchestrator.Observer = (*Server)(nil)

// New creates a [Server] with all API routes registered on an internal
// [http.ServeMux]. Does not start listening; call
// [Server.ListenAndServe].
func New(params Params) *Server {
	if params.SnapshotFn == nil {
		panic("server.New: SnapshotFn must not be nil")
	}
	if params.RefreshFn == nil {
		panic("server.New: RefreshFn must not be nil")
	}

	logger := params.Logger
	if logger == nil {
		logger = slog.Default()
	}

	mux := http.NewServeMux()

	s := &Server{
		mux:              mux,
		logger:           logger,
		snapshotFn:       params.SnapshotFn,
		refreshFn:        params.RefreshFn,
		retryNowFn:       params.RetryNowFn,
		acknowledgeFn:    params.AcknowledgeFn,
		checkNowFn:       params.CheckNowFn,
		workflowShapeFn:  params.WorkflowShapeFn,
		healthFn:         params.HealthFn,
		version:          params.Version,
		startedAt:        params.StartedAt,
		dbPingFn:         params.DBPingFn,
		preflightFn:      params.PreflightFn,
		workflowLoadedFn: params.WorkflowLoadedFn,
		runHistory:       params.RunHistory,
		tokenRates:       params.TokenRates,
		stats:            params.Stats,
		changes:          newChangeFeed(),
		timing: streamTiming{
			heartbeat:      15 * time.Second,
			signalInterval: time.Second,
			frameDeadline:  2 * time.Second,
		},
	}

	// Method-specific pattern so non-GET methods receive the default
	// 405 response from Go's ServeMux.
	mux.HandleFunc("GET /{$}", handleRoot)
	mux.HandleFunc("GET /favicon.ico", handleFavicon)
	mux.Handle("GET /ui/", ui.Handler())

	// Method-specific patterns ensure non-GET methods receive the
	// default 405 response from Go's ServeMux.
	mux.HandleFunc("GET /livez", s.handleLivez)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	// Method-agnostic patterns with internal method checking ensure
	// 405 responses use JSON error envelopes instead of the default
	// plain-text body from Go's ServeMux.
	mux.HandleFunc("/api/v1/state", s.routeState)
	mux.HandleFunc("/api/v1/runs", s.routeRuns)
	mux.HandleFunc("/api/v1/runs/issues", s.routeIssueGroups)
	mux.HandleFunc("/api/v1/stats", s.routeStats)
	mux.HandleFunc("/api/v1/refresh", s.routeRefresh)
	mux.HandleFunc("/api/v1/retries/{identifier}/run", s.routeRetryNow)
	mux.HandleFunc("/api/v1/attention/{id}/acknowledge", s.routeAcknowledge)
	mux.HandleFunc("/api/v1/reactions/{identifier}/{kind}/check", s.routeCheckNow)
	mux.HandleFunc("/api/v1/events", s.routeEvents)
	mux.HandleFunc("/api/v1/{identifier}", s.routeIssueDetail)

	if params.MetricsRegistry != nil {
		mux.Handle("GET /metrics", promhttp.InstrumentMetricHandler(
			params.MetricsRegistry,
			promhttp.HandlerFor(params.MetricsRegistry, promhttp.HandlerOpts{}),
		))
	}

	s.httpServer = &http.Server{
		Addr:              params.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.httpServer.RegisterOnShutdown(s.changes.close)

	return s
}

// Mux returns the underlying [*http.ServeMux] so callers can register
// additional routes beyond those registered by [New]. When
// [Params.MetricsRegistry] is non-nil, New registers GET /metrics
// internally; Mux remains available for any further external routes.
func (s *Server) Mux() *http.ServeMux {
	return s.mux
}

// ListenAndServe starts the HTTP listener. Blocks until the server is
// shut down or an unrecoverable error occurs. Returns
// [http.ErrServerClosed] on graceful shutdown.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Serve accepts connections on the provided [net.Listener]. Use this
// instead of [Server.ListenAndServe] when the listener is pre-bound
// (e.g. to discover the actual port for ephemeral port 0).
func (s *Server) Serve(ln net.Listener) error {
	return s.httpServer.Serve(ln)
}

// Shutdown gracefully shuts down the server without interrupting
// in-flight requests. Sets the draining flag as defense-in-depth
// before delegating to [http.Server.Shutdown]. Every open event stream
// ends as soon as shutdown begins, so its connection stops counting as
// active.
func (s *Server) Shutdown(ctx context.Context) error {
	s.drainingFlag.Store(true)
	return s.httpServer.Shutdown(ctx)
}

// SetDraining marks the server as draining. After this call, /livez
// and /readyz return 503. The listener remains open so K8s probes
// receive HTTP responses rather than connection refused. Safe to call
// from any goroutine.
func (s *Server) SetDraining() {
	s.drainingFlag.Store(true)
}

// OnStateChange satisfies [orchestrator.Observer] and tells every open event
// stream that something the API serves has changed. It performs no I/O,
// never blocks, and waits on nothing, so it is safe for concurrent use from
// any goroutine, before [Server.Serve] and after [Server.Shutdown].
func (s *Server) OnStateChange() {
	s.changes.publish()
}

// handleRoot redirects to the dashboard application. The header is written
// directly because [http.Redirect] adds a body to GET responses, and the
// status is 302 so that browsers do not cache a redirect that may change.
func handleRoot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Location", "/ui/")
	w.WriteHeader(http.StatusFound)
}
