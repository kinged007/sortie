package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/orchestrator"
	"github.com/sortie-ai/sortie/internal/redact"
	"github.com/sortie-ai/sortie/internal/sentence"
)

const (
	defaultRunsLimit = 50
	maxRunsLimit     = 200

	// issueHistoryPageSize is how many runs the per-issue history carries
	// before it hands the rest to GET /api/v1/runs.
	issueHistoryPageSize = 50

	// noRuleFilter selects the runs that record no rule name; no rule name
	// can take it.
	noRuleFilter = "<none>"
)

// runsResponse is the JSON wire format for GET /api/v1/runs.
type runsResponse struct {
	Runs       []runResponse `json:"runs"`
	NextCursor *string       `json:"next_cursor"`
}

// runResponse is the JSON wire format for one finished run. The token and
// origin members are null where the row does not record them.
type runResponse struct {
	RunID                int64               `json:"run_id"`
	IssueID              string              `json:"issue_id"`
	IssueIdentifier      string              `json:"issue_identifier"`
	DisplayIdentifier    string              `json:"display_identifier,omitempty"`
	IssueTitle           string              `json:"issue_title,omitempty"`
	IssueURL             string              `json:"issue_url,omitempty"`
	Attempt              int                 `json:"attempt"`
	Status               string              `json:"status"`
	Error                *string             `json:"error"`
	RuleName             string              `json:"rule_name,omitempty"`
	AgentKind            string              `json:"agent_kind,omitempty"`
	ConfiguredModel      string              `json:"configured_model,omitempty"`
	ConfiguredEffort     string              `json:"configured_effort,omitempty"`
	ModelName            string              `json:"model_name,omitempty"`
	WorkflowFile         string              `json:"workflow_file,omitempty"`
	StartedAt            string              `json:"started_at"`
	CompletedAt          string              `json:"completed_at"`
	DispatchedAt         *string             `json:"dispatched_at"`
	TurnsCompleted       int                 `json:"turns_completed"`
	Tokens               tokenInfo           `json:"tokens"`
	TokensMeasured       bool                `json:"tokens_measured"`
	UnaccountedTurns     int                 `json:"unaccounted_turns"`
	EstimatedCostUSD     *float64            `json:"estimated_cost_usd"`
	CostStatus           string              `json:"cost_status"`
	ChainID              string              `json:"chain_id,omitempty"`
	StagePrevious        string              `json:"stage_previous,omitempty"`
	StagePreviousOutcome string              `json:"stage_previous_outcome,omitempty"`
	StageTarget          string              `json:"stage_target,omitempty"`
	StageResult          string              `json:"stage_result,omitempty"`
	SelfReview           *selfReviewResponse `json:"self_review"`
	OriginRecorded       bool                `json:"origin_recorded"`
	Trigger              *string             `json:"trigger"`
	ReactionKind         *string             `json:"reaction_kind"`
	ReactionConfigKey    *string             `json:"reaction_config_key"`
	SessionID            *string             `json:"session_id"`
	SSHHost              *string             `json:"ssh_host"`

	// StageChain is the chain the run's rule stood on; null when the run has
	// no rule, its rule is on no chain, or no workflow shape is held.
	StageChain []string `json:"stage_chain"`

	// IssueOutcome is the issue's, the same on every run of the issue.
	IssueOutcome *issueOutcomeResponse `json:"issue_outcome"`
}

// issueOutcomeResponse is how an issue's latest work ended. State is present
// for a handed-off or terminal outcome; MergePath and PullRequest are present
// for a merged one.
type issueOutcomeResponse struct {
	Kind        string               `json:"kind"`
	ObservedAt  string               `json:"observed_at"`
	State       *string              `json:"state,omitempty"`
	MergePath   string               `json:"merge_path,omitempty"`
	PullRequest *pullRequestResponse `json:"pull_request,omitempty"`

	StatusText     string `json:"status_text"`
	StatusTextFull string `json:"status_text_full"`
}

// selfReviewResponse is the self-review outcome of one run.
type selfReviewResponse struct {
	FinalVerdict    string `json:"final_verdict"`
	TotalIterations int    `json:"total_iterations"`
	CapReached      bool   `json:"cap_reached"`
}

// issueHistoryResponse is the history member of GET /api/v1/{identifier}: an
// issue's newest runs, its totals over all runs, the hop decisions of the
// runs served, and the stage chain its last stage run stood on.
type issueHistoryResponse struct {
	Runs       []runResponse         `json:"runs"`
	NextCursor *string               `json:"next_cursor"`
	Totals     historyTotalsResponse `json:"totals"`
	Hops       []hopDecisionResponse `json:"hops"`
	LastStage  *lastStageResponse    `json:"last_stage"`

	IssueOutcome *issueOutcomeResponse `json:"issue_outcome"`
}

// lastStageResponse is a stage chain and the rule on it where a run stood.
type lastStageResponse struct {
	Chain []string `json:"chain"`
	Rule  string   `json:"rule"`
}

// historyTotalsResponse is an issue's spend over all of its runs.
type historyTotalsResponse struct {
	RunCount         int       `json:"run_count"`
	UnmeasuredRuns   int       `json:"unmeasured_runs"`
	Tokens           tokenInfo `json:"tokens"`
	TokensComplete   bool      `json:"tokens_complete"`
	EstimatedCostUSD *float64  `json:"estimated_cost_usd"`
	CostStatus       string    `json:"cost_status"`
}

// hopDecisionResponse is the target rule and result one run's exit reached.
type hopDecisionResponse struct {
	RunID       int64  `json:"run_id"`
	ChainID     string `json:"chain_id,omitempty"`
	SourceRule  string `json:"source_rule"`
	TargetRule  string `json:"target_rule"`
	Result      string `json:"result"`
	CompletedAt string `json:"completed_at"`
}

// paramError reports a query parameter the route rejects.
type paramError struct {
	param string
	rule  string
}

func (e *paramError) Error() string {
	return fmt.Sprintf("parameter %q %s", e.param, e.rule)
}

var runsParams = []string{"issue", "issue_id", "rule", "status", "stage", "since", "until", "ended_since", "ended_until", "cursor", "limit"}

// checkParamNames returns a *paramError for the first name, in sorted order,
// that is not in allowed or is given more than once.
func checkParamNames(values url.Values, allowed []string) error {
	for _, name := range slices.Sorted(maps.Keys(values)) {
		if !slices.Contains(allowed, name) {
			return &paramError{name, "is not a parameter of this route"}
		}
		if len(values[name]) > 1 {
			return &paramError{name, "is given more than once"}
		}
	}
	return nil
}

// parseRunsQuery validates every parameter of GET /api/v1/runs before any
// read, so a typo never serves unfiltered rows as filtered ones. An empty
// value reads as absent.
func parseRunsQuery(values url.Values) (RunHistoryQuery, error) {
	if err := checkParamNames(values, runsParams); err != nil {
		return RunHistoryQuery{}, err
	}

	q := RunHistoryQuery{
		Identifier:  values.Get("issue"),
		IssueID:     values.Get("issue_id"),
		Status:      values.Get("status"),
		StageTarget: values.Get("stage"),
		Limit:       defaultRunsLimit,
	}

	if v := values.Get("limit"); v != "" {
		limit, err := strconv.Atoi(v)
		if err != nil || limit < 1 || limit > maxRunsLimit {
			return RunHistoryQuery{}, &paramError{"limit", "must be an integer from 1 to 200"}
		}
		q.Limit = limit
	}
	if v := values.Get("cursor"); v != "" {
		cursor, err := strconv.ParseInt(v, 10, 64)
		if err != nil || cursor < 1 {
			return RunHistoryQuery{}, &paramError{"cursor", "must be an integer of at least 1"}
		}
		q.BeforeID = cursor
	}
	for _, bound := range []struct {
		name string
		dest **time.Time
	}{{"since", &q.Since}, {"until", &q.Until}, {"ended_since", &q.EndedSince}, {"ended_until", &q.EndedUntil}} {
		v := values.Get(bound.name)
		if v == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return RunHistoryQuery{}, &paramError{bound.name, "must be an RFC 3339 timestamp"}
		}
		*bound.dest = &at
	}
	if q.Since != nil && q.Until != nil && !q.Since.Before(*q.Until) {
		return RunHistoryQuery{}, &paramError{"since", "must be before until"}
	}
	if q.EndedSince != nil && q.EndedUntil != nil && !q.EndedSince.Before(*q.EndedUntil) {
		return RunHistoryQuery{}, &paramError{"ended_since", "must be before ended_until"}
	}

	if v := values.Get("rule"); v == noRuleFilter {
		q.RuleName = new("")
	} else if v != "" {
		q.RuleName = new(v)
	}
	return q, nil
}

// toRunResponse converts a run to its wire format, masking the free-text
// members at serve time so a value registered as a secret after the run is
// masked too. chains are the dispatch chains of the configuration in effect.
func toRunResponse(e RunHistoryEntry, rates TokenRates, chains [][]string) runResponse {
	resp := runResponse{
		RunID:                e.ID,
		IssueID:              e.IssueID,
		IssueIdentifier:      e.Identifier,
		DisplayIdentifier:    e.DisplayID,
		IssueTitle:           redact.Mask(e.IssueTitle),
		IssueURL:             redact.Mask(e.IssueURL),
		Attempt:              e.Attempt,
		Status:               e.Status,
		RuleName:             e.RuleName,
		AgentKind:            e.AgentKind,
		ConfiguredModel:      e.ConfiguredModel,
		ConfiguredEffort:     e.ConfiguredEffort,
		ModelName:            e.ReportedModel,
		WorkflowFile:         e.WorkflowFile,
		StartedAt:            e.StartedAt,
		CompletedAt:          e.CompletedAt,
		TurnsCompleted:       e.TurnsCompleted,
		TokensMeasured:       e.TokensMeasured,
		UnaccountedTurns:     e.UnaccountedTurns,
		ChainID:              e.ChainID,
		StagePrevious:        e.StagePrevious,
		StagePreviousOutcome: e.StagePreviousOutcome,
		StageTarget:          e.StageTarget,
		StageResult:          e.StageResult,
		SelfReview:           toSelfReviewResponse(e.ReviewMetadata),
		OriginRecorded:       e.OriginRecorded,
		StageChain:           orchestrator.StageChainOf(chains, e.RuleName, e.StagePrevious),
	}
	if e.Error != nil {
		resp.Error = new(redact.Mask(*e.Error))
	}
	if e.DispatchedAt != "" {
		resp.DispatchedAt = new(e.DispatchedAt)
	}
	if e.TokensMeasured {
		resp.Tokens = tokenInfo{
			InputTokens:      new(e.Tokens.InputTokens),
			OutputTokens:     new(e.Tokens.OutputTokens),
			TotalTokens:      new(e.Tokens.TotalTokens),
			CacheReadTokens:  new(e.Tokens.CacheReadTokens),
			CacheWriteTokens: new(e.Tokens.CacheWriteTokens),
		}
	}
	resp.EstimatedCostUSD, resp.CostStatus = runCostOf(e, rates)

	if e.OriginRecorded {
		resp.Trigger = copyText(e.Trigger)
		resp.ReactionKind = new(e.ReactionKind)
		resp.ReactionConfigKey = new(orchestrator.ReactionConfigKey(e.ReactionKind))
		resp.SessionID = new(e.SessionID)
		resp.SSHHost = new(e.SSHHost)
	}
	return resp
}

// copyText returns a pointer to a copy of *s, or nil for nil.
func copyText(s *string) *string {
	if s == nil {
		return nil
	}
	return new(*s)
}

// toSelfReviewResponse decodes the recorded review metadata; metadata that
// is absent or does not decode reads as no self-review.
func toSelfReviewResponse(metadata *string) *selfReviewResponse {
	if metadata == nil {
		return nil
	}
	var review domain.ReviewMetadata
	if err := json.Unmarshal([]byte(*metadata), &review); err != nil {
		return nil
	}
	verdict := review.FinalVerdict
	if verdict == "" {
		verdict = "none"
	}
	return &selfReviewResponse{
		FinalVerdict:    redact.Mask(verdict),
		TotalIterations: review.TotalIterations,
		CapReached:      review.CapReached,
	}
}

func toRunResponses(rows []RunHistoryEntry, rates TokenRates, chains [][]string) []runResponse {
	runs := make([]runResponse, len(rows))
	for i, row := range rows {
		runs[i] = toRunResponse(row, rates, chains)
	}
	return runs
}

// toIssueOutcomeResponse converts an outcome to its wire format, masking the
// free-text members at serve time. The identifiers name the issue in the
// sentence of how it ended.
func toIssueOutcomeResponse(o IssueOutcome, identifier, displayID string) *issueOutcomeResponse {
	outcome := sentenceOutcomeOf(o)
	resp := &issueOutcomeResponse{Kind: o.Kind, ObservedAt: o.ObservedAt}
	if o.Kind == "merged" {
		resp.MergePath = outcome.MergePath
		resp.PullRequest = &pullRequestResponse{Number: o.PRNumber, Repository: redact.Mask(o.PRRepository)}
	} else {
		resp.State = new(outcome.State)
	}
	finished := &sentence.Finished{Identifier: identifier, DisplayIdentifier: displayID, Outcome: &outcome}
	resp.StatusText, resp.StatusTextFull = sentencesOf(sentence.Entry{Finished: finished}, sentence.Context{})
	return resp
}

// sentenceOutcomeOf is the outcome as the sentence table reads it: the
// members the API serves for the outcome's kind, masked as served.
func sentenceOutcomeOf(o IssueOutcome) sentence.Outcome {
	if o.Kind == "merged" {
		return sentence.Outcome{Kind: o.Kind, MergePath: orchestrator.ReactionConfigKey(o.MergeReactionKind), PRNumber: o.PRNumber}
	}
	return sentence.Outcome{Kind: o.Kind, State: redact.Mask(o.State)}
}

// finishedOf describes an issue no live entry holds by its newest run's hop
// decision and its outcome; hasOutcome is false when Sortie recorded none.
func finishedOf(newest RunHistoryEntry, o IssueOutcome, hasOutcome bool) *sentence.Finished {
	finished := &sentence.Finished{
		Identifier:        newest.Identifier,
		DisplayIdentifier: newest.DisplayID,
		StageTarget:       newest.StageTarget,
		StageResult:       newest.StageResult,
	}
	if hasOutcome {
		outcome := sentenceOutcomeOf(o)
		finished.Outcome = &outcome
	}
	return finished
}

// attachIssueOutcomes sets each run's issue_outcome from the outcome of its
// issue; a run whose issue has none keeps null.
func attachIssueOutcomes(rows []runResponse, outcomes map[string]IssueOutcome) {
	for i := range rows {
		if o, ok := outcomes[rows[i].IssueID]; ok {
			rows[i].IssueOutcome = toIssueOutcomeResponse(o, rows[i].IssueIdentifier, rows[i].DisplayIdentifier)
		}
	}
}

// distinctIssueIDs lists the issue IDs of rows once each, in first-seen order.
func distinctIssueIDs(rows []RunHistoryEntry) []string {
	seen := make(map[string]struct{}, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, dup := seen[row.IssueID]; dup {
			continue
		}
		seen[row.IssueID] = struct{}{}
		ids = append(ids, row.IssueID)
	}
	return ids
}

// historyTotalsOf converts an issue's totals to their wire format. A token
// sum that is zero while the spend is incomplete serves null, because the
// zero would stand in for spend Sortie does not know.
func historyTotalsOf(t IssueRunTotals, rates TokenRates) historyTotalsResponse {
	complete := t.Usage.UnmeasuredSessions == 0 && t.Usage.UnaccountedTurns == 0
	known := func(sum int64) *int64 {
		if sum == 0 && !complete {
			return nil
		}
		return &sum
	}
	tokens := tokenInfo{
		InputTokens:      known(t.Tokens.InputTokens),
		OutputTokens:     known(t.Tokens.OutputTokens),
		TotalTokens:      known(t.Usage.TotalTokens),
		CacheReadTokens:  known(t.Tokens.CacheReadTokens),
		CacheWriteTokens: known(t.Tokens.CacheWriteTokens),
	}
	cost, status := issueCostFromKinds(t.Usage.ByKind, tokens.TotalTokens, complete, sessionCostPart{}, rates)
	return historyTotalsResponse{
		RunCount:         t.Usage.Sessions,
		UnmeasuredRuns:   t.Usage.UnmeasuredSessions,
		Tokens:           tokens,
		TokensComplete:   complete,
		EstimatedCostUSD: cost,
		CostStatus:       status,
	}
}

// hopRowsOf lists the hop decisions of rows, keeping their order.
func hopRowsOf(rows []RunHistoryEntry) []hopDecisionResponse {
	hops := []hopDecisionResponse{}
	for _, row := range rows {
		if row.StageTarget == "" {
			continue
		}
		hops = append(hops, hopDecisionResponse{
			RunID:       row.ID,
			ChainID:     row.ChainID,
			SourceRule:  row.RuleName,
			TargetRule:  row.StageTarget,
			Result:      row.StageResult,
			CompletedAt: row.CompletedAt,
		})
	}
	return hops
}

// cutPage cuts a read that fetched one run past limit to limit runs and
// names the cursor that continues after the last of them.
func cutPage(rows []RunHistoryEntry, limit int) ([]RunHistoryEntry, *string) {
	if len(rows) <= limit {
		return rows, nil
	}
	rows = rows[:limit]
	return rows, new(strconv.FormatInt(rows[limit-1].ID, 10))
}

func (s *Server) routeRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.methodNotAllowed(w, r, http.MethodGet)
		return
	}
	s.handleRuns(w, r)
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	if s.runHistory == nil {
		writeHistoryUnavailable(w, s.logger)
		return
	}

	q, err := parseRunsQuery(r.URL.Query())
	if err != nil {
		s.logger.Debug("run history request rejected",
			slog.String("path", r.URL.Path),
			slog.Any("error", err),
		)
		writeErrorJSON(w, s.logger, http.StatusBadRequest, "invalid_parameter", err.Error())
		return
	}

	limit := q.Limit
	q.Limit = limit + 1
	rows, err := s.runHistory.QueryRuns(r.Context(), q)
	if err != nil {
		s.historyReadFailed(w, r, err)
		return
	}

	rows, next := cutPage(rows, limit)
	outcomes, err := s.runHistory.IssueOutcomes(r.Context(), distinctIssueIDs(rows))
	if err != nil {
		s.historyReadFailed(w, r, err)
		return
	}
	var chains [][]string
	if s.workflowShapeFn != nil {
		chains = s.workflowShapeFn().Dispatch.Chains
	}
	runs := toRunResponses(rows, s.tokenRates, chains)
	attachIssueOutcomes(runs, outcomes)
	writeJSON(w, s.logger, http.StatusOK, runsResponse{Runs: runs, NextCursor: next})

	s.logger.Debug("request served",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", http.StatusOK),
		slog.Duration("duration", time.Since(start)),
	)
}

func writeHistoryUnavailable(w http.ResponseWriter, logger *slog.Logger) {
	writeErrorJSON(w, logger, http.StatusServiceUnavailable,
		"history_unavailable",
		"run history unavailable")
}

// historyReadFailed answers a failed run-history read with 503 and logs it
// at Warn, so a failed read is never served as an issue without runs.
func (s *Server) historyReadFailed(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Warn("run history request failed",
		slog.Any("error", err),
		slog.String("path", r.URL.Path),
	)
	writeHistoryUnavailable(w, s.logger)
}
