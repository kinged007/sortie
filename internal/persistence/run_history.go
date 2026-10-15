package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const reactionRecoveryMaxCandidates = 200

// HandoffAbsenceErrorPrefix marks a run_history error where a handoff was
// withheld because no work was observed. Keeping it stable lets the retry
// and polling paths reconstruct the consecutive-absence sequence without a
// verdict column.
const HandoffAbsenceErrorPrefix = "handoff withheld: "

// RunHistory is a single completed run attempt in the run_history table.
// ID is assigned by the database on insert; leave it zero when calling
// [Store.AppendRunHistory].
type RunHistory struct {
	ID             int64
	IssueID        string
	Identifier     string
	DisplayID      string // Qualified form (e.g. "owner/repo#9"); empty when Identifier suffices.
	Attempt        int
	AgentAdapter   string
	Workspace      string
	StartedAt      string
	CompletedAt    string
	Status         string // "succeeded", "failed", "cancelled", "ci_failed", "needs_person", or "budget_stopped".
	Error          *string
	WorkflowFile   string // Base filename; empty for pre-migration rows.
	TurnsCompleted int
	ReviewMetadata *string // JSON ReviewMetadata; nil when self-review did not run.
	RuleName       string  // Empty for legacy rows and fallback dispatches.
	TemplateID     string  // Empty for legacy rows and the workflow body template.

	// ConfiguredModel and ConfiguredEffort are what the attempt was
	// configured with; ReportedModel is what the runtime reported running.
	// Each is empty when unset, and for pre-migration rows.
	ConfiguredModel  string
	ConfiguredEffort string
	ReportedModel    string

	InputTokens      int64 // 0 for pre-migration rows.
	OutputTokens     int64 // 0 for pre-migration rows.
	TotalTokens      int64 // 0 for pre-migration rows.
	CacheReadTokens  int64 // 0 for pre-migration rows.
	CacheWriteTokens int64 // 0 for pre-migration rows.

	// TokensMeasured is true when the token columns carry a runtime-reported
	// figure and false when spend is unknown rather than zero. Every writer
	// must set it explicitly: the column's SQL default is 1, so an omitted
	// field records an unmeasured run rather than inheriting the default.
	TokensMeasured bool

	// UnaccountedTurns counts turns that spent tokens no figure was proven
	// to cover; that overspend reaches none of the token columns, so a
	// non-zero count makes this row's total a lower bound. Orthogonal to
	// TokensMeasured.
	UnaccountedTurns int

	// StagePrevious and StagePreviousOutcome are the frozen stage.previous
	// pair the attempt rendered with; both are empty when no hop led to the
	// dispatch and for pre-migration rows.
	StagePrevious        string
	StagePreviousOutcome string

	// ChainID groups the runs of one pass through a chain of stages; it is
	// empty only for rows written before migration 024.
	ChainID string

	// StageTarget and StageResult record the hop decision the attempt's
	// exit reached; both are empty when it reached none. StageResult is
	// "advanced", "partial", "failed", or "ceiling".
	StageTarget string
	StageResult string

	// IssueTitle and IssueURL are the tracker's title and web link as read
	// when the attempt was dispatched; both are empty for rows written
	// before migration 025.
	IssueTitle string
	IssueURL   string

	// DispatchedAt is the RFC 3339 start of the first run of the pass
	// ChainID names; empty when the pass could not be dated.
	DispatchedAt string

	// Trigger is the sortie_retries_total label of the schedule that
	// started the retry whose fire dispatched the run; empty for a poll-tick
	// dispatch and for a retry a reaction other than the CI fix started. Nil
	// reads and writes NULL: the retry never stored what started it, or the
	// row predates migration 028.
	Trigger *string

	// ReactionKind is the dispatching retry's reaction kind; empty when none.
	ReactionKind string

	// SessionID is the agent session the run used; empty when the run
	// started none.
	SessionID string

	// SSHHost is the SSH worker the run executed on; empty for a local run.
	SSHHost string

	// OriginRecorded is false when session_id is NULL, which only a row
	// written before migration 028 holds. It is ignored on write.
	OriginRecorded bool

	// FirstAfterHop is true on the first run dispatched after the stage hop
	// StagePrevious names, and false on every other run.
	FirstAfterHop bool
}

// AppendRunHistory inserts a completed run attempt. The input ID is
// ignored; the returned record has the database-assigned ID.
func (s *Store) AppendRunHistory(ctx context.Context, run RunHistory) (RunHistory, error) {
	errVal := sql.NullString{}
	if run.Error != nil {
		errVal = sql.NullString{String: *run.Error, Valid: true}
	}

	wfVal := sql.NullString{}
	if run.WorkflowFile != "" {
		wfVal = sql.NullString{String: run.WorkflowFile, Valid: true}
	}

	dispIDVal := sql.NullString{}
	if run.DisplayID != "" {
		dispIDVal = sql.NullString{String: run.DisplayID, Valid: true}
	}

	reviewMetaVal := sql.NullString{}
	if run.ReviewMetadata != nil {
		reviewMetaVal = sql.NullString{String: *run.ReviewMetadata, Valid: true}
	}

	triggerVal := sql.NullString{}
	if run.Trigger != nil {
		triggerVal = sql.NullString{String: *run.Trigger, Valid: true}
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO run_history
			(issue_id, identifier, display_identifier, attempt, agent_adapter, workspace, started_at, completed_at, status, error, workflow_file, turns_completed, review_metadata, rule_name, template_id, input_tokens, output_tokens, total_tokens, cache_read_tokens, cache_write_tokens, tokens_measured, unaccounted_turns, configured_model, configured_effort, reported_model, stage_previous, stage_previous_outcome, chain_id, stage_target, stage_result, issue_title, issue_url, dispatched_at, trigger, reaction_kind, session_id, ssh_host, first_after_hop)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.IssueID, run.Identifier, dispIDVal, run.Attempt, run.AgentAdapter,
		run.Workspace, run.StartedAt, run.CompletedAt, run.Status, errVal, wfVal,
		run.TurnsCompleted, reviewMetaVal, run.RuleName, run.TemplateID,
		run.InputTokens, run.OutputTokens, run.TotalTokens, run.CacheReadTokens, run.CacheWriteTokens, run.TokensMeasured,
		run.UnaccountedTurns, run.ConfiguredModel, run.ConfiguredEffort, run.ReportedModel,
		run.StagePrevious, run.StagePreviousOutcome, run.ChainID, run.StageTarget, run.StageResult,
		run.IssueTitle, run.IssueURL, run.DispatchedAt,
		triggerVal, run.ReactionKind, run.SessionID, run.SSHHost, run.FirstAfterHop,
	)
	if err != nil {
		return RunHistory{}, fmt.Errorf("append run history for %q: %w", run.IssueID, err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return RunHistory{}, fmt.Errorf("append run history last insert id: %w", err)
	}
	run.ID = id
	return run, nil
}

// QueryRunHistoryByIssue returns all entries for the issue, ordered by id
// descending. Returns an empty non-nil slice when none exist.
func (s *Store) QueryRunHistoryByIssue(ctx context.Context, issueID string) ([]RunHistory, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, issue_id, identifier, display_identifier, attempt, agent_adapter, workspace,
			started_at, completed_at, status, error, workflow_file, turns_completed, review_metadata, rule_name, template_id,
			input_tokens, output_tokens, total_tokens, cache_read_tokens, cache_write_tokens, tokens_measured, unaccounted_turns,
			configured_model, configured_effort, reported_model
		FROM run_history
		WHERE issue_id = ?
		ORDER BY id DESC`, issueID)
	if err != nil {
		return nil, fmt.Errorf("query run history by issue %q: %w", issueID, err)
	}
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	entries := []RunHistory{}
	for rows.Next() {
		var r RunHistory
		var errVal, wfVal, dispIDVal, reviewMetaVal sql.NullString
		if err := rows.Scan(
			&r.ID, &r.IssueID, &r.Identifier, &dispIDVal, &r.Attempt, &r.AgentAdapter,
			&r.Workspace, &r.StartedAt, &r.CompletedAt, &r.Status, &errVal, &wfVal,
			&r.TurnsCompleted, &reviewMetaVal, &r.RuleName, &r.TemplateID,
			&r.InputTokens, &r.OutputTokens, &r.TotalTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &r.TokensMeasured,
			&r.UnaccountedTurns, &r.ConfiguredModel, &r.ConfiguredEffort, &r.ReportedModel,
		); err != nil {
			return nil, fmt.Errorf("scan run history: %w", err)
		}
		if errVal.Valid {
			r.Error = new(errVal.String)
		}
		if wfVal.Valid {
			r.WorkflowFile = wfVal.String
		}
		if dispIDVal.Valid {
			r.DisplayID = dispIDVal.String
		}
		if reviewMetaVal.Valid {
			r.ReviewMetadata = new(reviewMetaVal.String)
		}
		entries = append(entries, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query run history by issue: %w", err)
	}
	return entries, nil
}

// reactionRecoveryRunColumns is the column list of the queries that load a
// run row for rebuilding a pending reaction, read by [scanRecoveryRun].
const reactionRecoveryRunColumns = `r.id, r.issue_id, r.identifier, r.display_identifier, r.attempt, r.agent_adapter,
			r.workspace, r.started_at, r.completed_at, r.status, r.error, r.workflow_file,
			r.turns_completed, r.review_metadata, r.rule_name, r.template_id,
			r.input_tokens, r.output_tokens, r.total_tokens, r.cache_read_tokens, r.cache_write_tokens, r.tokens_measured,
			r.unaccounted_turns, r.configured_model, r.configured_effort, r.reported_model,
			r.stage_previous, r.stage_previous_outcome, r.chain_id,
			r.issue_title, r.issue_url, r.dispatched_at`

// scanRecoveryRun reads one row selected with [reactionRecoveryRunColumns].
func scanRecoveryRun(rows *sql.Rows) (RunHistory, error) {
	var run RunHistory
	var errVal, wfVal, dispIDVal, reviewMetaVal sql.NullString
	if err := rows.Scan(
		&run.ID, &run.IssueID, &run.Identifier, &dispIDVal, &run.Attempt, &run.AgentAdapter,
		&run.Workspace, &run.StartedAt, &run.CompletedAt, &run.Status, &errVal, &wfVal,
		&run.TurnsCompleted, &reviewMetaVal, &run.RuleName, &run.TemplateID,
		&run.InputTokens, &run.OutputTokens, &run.TotalTokens, &run.CacheReadTokens, &run.CacheWriteTokens, &run.TokensMeasured,
		&run.UnaccountedTurns, &run.ConfiguredModel, &run.ConfiguredEffort, &run.ReportedModel,
		&run.StagePrevious, &run.StagePreviousOutcome, &run.ChainID,
		&run.IssueTitle, &run.IssueURL, &run.DispatchedAt,
	); err != nil {
		return RunHistory{}, err
	}
	if errVal.Valid {
		run.Error = new(errVal.String)
	}
	if wfVal.Valid {
		run.WorkflowFile = wfVal.String
	}
	if dispIDVal.Valid {
		run.DisplayID = dispIDVal.String
	}
	if reviewMetaVal.Valid {
		run.ReviewMetadata = new(reviewMetaVal.String)
	}
	return run, nil
}

// LoadLatestSuccessfulRunsForReactionRecovery returns at most limit latest
// successful rows, one per issue, for rows with non-empty workspace and
// completed_at >= completedAfter, ordered by descending id. limit is
// clamped to the recovery maximum. Returns an empty non-nil slice when
// none qualify.
func (s *Store) LoadLatestSuccessfulRunsForReactionRecovery(ctx context.Context, completedAfter time.Time, limit int) ([]RunHistory, error) {
	if limit <= 0 {
		limit = 1
	}
	if limit > reactionRecoveryMaxCandidates {
		limit = reactionRecoveryMaxCandidates
	}

	rows, err := s.db.QueryContext(ctx,
		`WITH latest AS (
			SELECT issue_id, MAX(id) AS latest_id
			FROM run_history
			WHERE status = 'succeeded'
			  AND workspace <> ''
			  AND completed_at >= ?
			GROUP BY issue_id
		), bounded AS (
			SELECT latest_id
			FROM latest
			ORDER BY latest_id DESC
			LIMIT ?
		)
		SELECT `+reactionRecoveryRunColumns+`
		FROM run_history AS r
		JOIN bounded ON bounded.latest_id = r.id
		ORDER BY r.id DESC`, completedAfter.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, fmt.Errorf("load recovery runs: %w", err)
	}
	return collectRecoveryRuns(rows)
}

// LoadLatestSuccessfulRunsForIssues returns, for each given issue, its
// latest successful row with a non-empty workspace, whatever its age,
// ordered by descending id. Returns an empty non-nil slice, without a
// query, when issueIDs is empty.
func (s *Store) LoadLatestSuccessfulRunsForIssues(ctx context.Context, issueIDs []string) ([]RunHistory, error) {
	if len(issueIDs) == 0 {
		return []RunHistory{}, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(issueIDs)), ",")
	args := make([]any, len(issueIDs))
	for i, id := range issueIDs {
		args[i] = id
	}

	query := fmt.Sprintf( //nolint:gosec // placeholders is "?,?,..." built from len(issueIDs); the column list is a constant
		`WITH latest AS (
			SELECT MAX(id) AS latest_id
			FROM run_history
			WHERE status = 'succeeded'
			  AND workspace <> ''
			  AND issue_id IN (%s)
			GROUP BY issue_id
		)
		SELECT %s
		FROM run_history AS r
		JOIN latest ON latest.latest_id = r.id
		ORDER BY r.id DESC`,
		placeholders, reactionRecoveryRunColumns,
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load latest runs for issues: %w", err)
	}
	return collectRecoveryRuns(rows)
}

// collectRecoveryRuns drains rows selected with [reactionRecoveryRunColumns]
// and closes them.
func collectRecoveryRuns(rows *sql.Rows) ([]RunHistory, error) {
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	entries := []RunHistory{}
	for rows.Next() {
		run, err := scanRecoveryRun(rows)
		if err != nil {
			return nil, fmt.Errorf("load recovery runs: %w", err)
		}
		entries = append(entries, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load recovery runs: %w", err)
	}
	return entries, nil
}

// RunHistoryFilter selects run_history rows; a zero field matches every row.
type RunHistoryFilter struct {
	IssueID    string
	Identifier string

	// RuleName nil matches every row; a pointer to "" selects the rows whose
	// rule_name is empty.
	RuleName    *string
	Status      string
	StageTarget string

	// CompletedSince keeps rows completed at or after it and CompletedBefore
	// rows completed before it, both compared as instants.
	CompletedSince  *time.Time
	CompletedBefore *time.Time

	// EndedSince keeps rows whose issue's outcome was observed at or after it
	// and EndedBefore rows whose issue's outcome was observed before it, both
	// compared as instants the way CompletedSince compares completed_at. A
	// row whose issue has no outcome matches no ending bound.
	EndedSince  *time.Time
	EndedBefore *time.Time

	// BeforeID keeps rows with an id below it; 0 starts at the newest row.
	BeforeID int64

	// Limit is the most rows returned; below 1 reads as 1.
	Limit int
}

// QueryRunHistory returns the rows matching every non-zero field of f,
// ordered by id descending, or an empty non-nil slice when none match. A row
// whose completed_at does not parse as a timestamp never matches a completion
// bound, and an outcome whose observed_at does not parse never matches an
// ending bound.
func (s *Store) QueryRunHistory(ctx context.Context, f RunHistoryFilter) ([]RunHistory, error) {
	var conds []string
	var args []any
	if f.IssueID != "" {
		conds = append(conds, "issue_id = ?")
		args = append(args, f.IssueID)
	}
	if f.Identifier != "" {
		conds = append(conds, "identifier = ?")
		args = append(args, f.Identifier)
	}
	if f.RuleName != nil {
		conds = append(conds, "rule_name = ?")
		args = append(args, *f.RuleName)
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}
	if f.StageTarget != "" {
		conds = append(conds, "stage_target = ?")
		args = append(args, f.StageTarget)
	}
	if f.CompletedSince != nil {
		conds = append(conds, "unixepoch(completed_at, 'subsec') >= unixepoch(?, 'subsec')")
		args = append(args, f.CompletedSince.UTC().Format(time.RFC3339Nano))
	}
	if f.CompletedBefore != nil {
		conds = append(conds, "unixepoch(completed_at, 'subsec') < unixepoch(?, 'subsec')")
		args = append(args, f.CompletedBefore.UTC().Format(time.RFC3339Nano))
	}
	if f.EndedSince != nil {
		conds = append(conds, "issue_id IN (SELECT issue_id FROM issue_outcomes WHERE unixepoch(observed_at, 'subsec') >= unixepoch(?, 'subsec'))")
		args = append(args, f.EndedSince.UTC().Format(time.RFC3339Nano))
	}
	if f.EndedBefore != nil {
		conds = append(conds, "issue_id IN (SELECT issue_id FROM issue_outcomes WHERE unixepoch(observed_at, 'subsec') < unixepoch(?, 'subsec'))")
		args = append(args, f.EndedBefore.UTC().Format(time.RFC3339Nano))
	}
	if f.BeforeID > 0 {
		conds = append(conds, "id < ?")
		args = append(args, f.BeforeID)
	}

	query := `SELECT id, issue_id, identifier, display_identifier, attempt, agent_adapter, workspace,
			started_at, completed_at, status, error, workflow_file, turns_completed, review_metadata, rule_name, template_id,
			input_tokens, output_tokens, total_tokens, cache_read_tokens, cache_write_tokens, tokens_measured, unaccounted_turns,
			configured_model, configured_effort, reported_model,
			chain_id, stage_previous, stage_previous_outcome, stage_target, stage_result,
			issue_title, issue_url, dispatched_at,
			trigger, reaction_kind, session_id, ssh_host
		FROM run_history`
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ") //nolint:gosec // conds holds only fixed predicates with ? placeholders; values are bound via args
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, max(f.Limit, 1))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query run history: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	entries := []RunHistory{}
	for rows.Next() {
		var r RunHistory
		var errVal, wfVal, dispIDVal, reviewMetaVal, triggerVal, reactionVal, sessionVal, hostVal sql.NullString
		if err := rows.Scan(
			&r.ID, &r.IssueID, &r.Identifier, &dispIDVal, &r.Attempt, &r.AgentAdapter,
			&r.Workspace, &r.StartedAt, &r.CompletedAt, &r.Status, &errVal, &wfVal,
			&r.TurnsCompleted, &reviewMetaVal, &r.RuleName, &r.TemplateID,
			&r.InputTokens, &r.OutputTokens, &r.TotalTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &r.TokensMeasured,
			&r.UnaccountedTurns, &r.ConfiguredModel, &r.ConfiguredEffort, &r.ReportedModel,
			&r.ChainID, &r.StagePrevious, &r.StagePreviousOutcome, &r.StageTarget, &r.StageResult,
			&r.IssueTitle, &r.IssueURL, &r.DispatchedAt,
			&triggerVal, &reactionVal, &sessionVal, &hostVal,
		); err != nil {
			return nil, fmt.Errorf("scan run history: %w", err)
		}
		if errVal.Valid {
			r.Error = new(errVal.String)
		}
		if wfVal.Valid {
			r.WorkflowFile = wfVal.String
		}
		if dispIDVal.Valid {
			r.DisplayID = dispIDVal.String
		}
		if reviewMetaVal.Valid {
			r.ReviewMetadata = new(reviewMetaVal.String)
		}
		if triggerVal.Valid {
			r.Trigger = new(triggerVal.String)
		}
		r.ReactionKind = reactionVal.String
		r.SessionID = sessionVal.String
		r.SSHHost = hostVal.String
		r.OriginRecorded = sessionVal.Valid
		entries = append(entries, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query run history: %w", err)
	}
	return entries, nil
}

// IssueGroupFilter selects the issues [Store.QueryIssueGroups] groups; a zero
// field matches every run.
type IssueGroupFilter struct {
	// IssueKey keeps the issues with a run whose identifier or
	// display_identifier equals it.
	IssueKey string

	// RuleName nil matches every run; a pointer to "" selects the runs whose
	// rule_name is empty.
	RuleName    *string
	Status      string
	StageTarget string

	// BeforeID continues after the group whose newest matching run has this
	// id; 0 starts at the newest group.
	BeforeID int64

	// Limit is the most groups returned; below 1 reads as 1.
	Limit int
}

// IssueGroup is one issue with a run the filter matched, and the id of its
// newest such run.
type IssueGroup struct {
	IssueID     string
	NewestRunID int64
}

// QueryIssueGroups returns the issues whose runs match f, each with the id of
// its newest matching run, newest first, or an empty non-nil slice when none
// match. With BeforeID set it skips every issue that has a matching run at or
// above it, so a traversal serves each issue once.
//
// The selection is one streamed statement that stops once it holds the page,
// and no other read runs while it is open: the store holds one connection, so
// a nested read would wait on it forever.
func (s *Store) QueryIssueGroups(ctx context.Context, f IssueGroupFilter) ([]IssueGroup, error) {
	var keyed []string
	if f.IssueKey != "" {
		ids, err := s.issueIDsByKey(ctx, f.IssueKey)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []IssueGroup{}, nil
		}
		keyed = ids
	}
	return s.streamIssueGroups(ctx, f, keyed)
}

// issueIDsByKey returns the issues with a run whose identifier or
// display_identifier equals key, and closes its statement before it returns.
func (s *Store) issueIDsByKey(ctx context.Context, key string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT issue_id FROM run_history WHERE identifier = ?
		UNION
		SELECT issue_id FROM run_history WHERE display_identifier = ?`, key, key)
	if err != nil {
		return nil, fmt.Errorf("query issue groups: resolve key: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("query issue groups: scan issue id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query issue groups: resolve key: %w", err)
	}
	return ids, nil
}

// issueGroupRunPredicates returns the fixed predicates of f's rule, status,
// and stage on the run_history table aliased as alias, with their bound
// values.
func issueGroupRunPredicates(alias string, f IssueGroupFilter) ([]string, []any) {
	var conds []string
	var args []any
	if f.RuleName != nil {
		conds = append(conds, alias+".rule_name = ?")
		args = append(args, *f.RuleName)
	}
	if f.Status != "" {
		conds = append(conds, alias+".status = ?")
		args = append(args, f.Status)
	}
	if f.StageTarget != "" {
		conds = append(conds, alias+".stage_target = ?")
		args = append(args, f.StageTarget)
	}
	return conds, args
}

// streamIssueGroups reads matching runs newest first and stops, closing its
// statement, once it holds the page of distinct issues. keyed, when non-nil,
// limits the runs to those issues.
func (s *Store) streamIssueGroups(ctx context.Context, f IssueGroupFilter, keyed []string) ([]IssueGroup, error) {
	conds, args := issueGroupRunPredicates("r", f)
	if keyed != nil {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keyed)), ",")
		conds = append([]string{"r.issue_id IN (" + placeholders + ")"}, conds...)
		keyedArgs := make([]any, 0, len(keyed)+len(args))
		for _, id := range keyed {
			keyedArgs = append(keyedArgs, id)
		}
		args = append(keyedArgs, args...)
	}
	if f.BeforeID > 0 {
		subConds, subArgs := issueGroupRunPredicates("s", f)
		subConds = append([]string{"s.issue_id = r.issue_id"}, subConds...)
		subConds = append(subConds, "s.id >= ?")
		conds = append(conds, "r.id < ?",
			"NOT EXISTS (SELECT 1 FROM run_history AS s WHERE "+strings.Join(subConds, " AND ")+")")
		args = append(args, f.BeforeID)
		args = append(args, subArgs...)
		args = append(args, f.BeforeID)
	}

	query := "SELECT r.id, r.issue_id FROM run_history AS r"
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ") //nolint:gosec // conds holds only fixed predicates with ? placeholders; values are bound via args
	}
	query += " ORDER BY r.id DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query issue groups: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	limit := max(f.Limit, 1)
	groups := []IssueGroup{}
	served := make(map[string]struct{}, limit)
	for len(groups) < limit && rows.Next() {
		var g IssueGroup
		if err := rows.Scan(&g.NewestRunID, &g.IssueID); err != nil {
			return nil, fmt.Errorf("query issue groups: scan: %w", err)
		}
		if _, dup := served[g.IssueID]; dup {
			continue
		}
		served[g.IssueID] = struct{}{}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query issue groups: %w", err)
	}
	return groups, nil
}

// RecordRunStageResult sets the hop decision on the run_history row with the
// given id. It is a no-op, not an error, when no row has that id.
func (s *Store) RecordRunStageResult(ctx context.Context, runID int64, targetRule, result string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE run_history SET stage_target = ?, stage_result = ? WHERE id = ?`,
		targetRule, result, runID)
	if err != nil {
		return fmt.Errorf("record run stage result %d: %w", runID, err)
	}
	return nil
}

// CountRunHistoryByIssue returns the number of entries for the issue, or
// (0, nil) when none exist.
func (s *Store) CountRunHistoryByIssue(ctx context.Context, issueID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_history WHERE issue_id = ?`, issueID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count run history by issue %q: %w", issueID, err)
	}
	return count, nil
}

// CountWorkerRunsCompletedSince returns the number of worker-session rows
// for the issue with completed_at at or after since, or (0, nil) when none
// qualify. "ci_failed" rows are excluded by name rather than by an
// inclusion list, so a status added later counts as a worker session by
// default.
func (s *Store) CountWorkerRunsCompletedSince(ctx context.Context, issueID string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_history
		WHERE issue_id = ?
		  AND status <> 'ci_failed'
		  AND completed_at >= ?`,
		issueID, since.UTC().Format(time.RFC3339),
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count worker runs completed since for issue %q: %w", issueID, err)
	}
	return count, nil
}

// QueryConsecutiveHandoffAbsenceCounts returns the handoff-absence failure
// count per requested issue since [Store.ResetHandoffAbsenceSequence] last
// ended that issue's sequence. Issues with no qualifying rows are omitted.
//
// Only a work-observed verdict resets the sequence; "succeeded" does not,
// because it is also recorded for outcomes carrying no verdict (a blocked
// soft stop, a run that does not drive issue state, an undeterminable run,
// every run under the off policy). Counting those as a reset would let an
// absence sequence alternate below the ceiling indefinitely.
func (s *Store) QueryConsecutiveHandoffAbsenceCounts(ctx context.Context, issueIDs []string) (map[string]int, error) {
	counts := make(map[string]int)
	if len(issueIDs) == 0 {
		return counts, nil
	}

	placeholders := strings.Repeat("?,", len(issueIDs))
	placeholders = placeholders[:len(placeholders)-1]

	args := make([]any, 0, len(issueIDs)+2)
	for _, id := range issueIDs {
		args = append(args, id)
	}
	args = append(args, len(HandoffAbsenceErrorPrefix), HandoffAbsenceErrorPrefix)

	query := fmt.Sprintf( //nolint:gosec // placeholders is built only from len(issueIDs); values remain bound parameters
		`SELECT r.issue_id, COUNT(*)
		 FROM run_history AS r
		 WHERE r.issue_id IN (%s)
		   AND r.status = 'failed'
		   AND substr(r.error, 1, ?) = ?
		   AND r.id > COALESCE((
		       SELECT reset.reset_run_id
		       FROM handoff_absence_resets AS reset
		       WHERE reset.issue_id = r.issue_id
		   ), 0)
		 GROUP BY r.issue_id`,
		placeholders,
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query consecutive handoff absences: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	for rows.Next() {
		var issueID string
		var count int
		if err := rows.Scan(&issueID, &count); err != nil {
			return nil, fmt.Errorf("scan consecutive handoff absences: %w", err)
		}
		counts[issueID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query consecutive handoff absences: %w", err)
	}
	return counts, nil
}

// ResetHandoffAbsenceSequence ends the issue's consecutive absence sequence
// at its most recent run, so [Store.QueryConsecutiveHandoffAbsenceCounts]
// reports zero until a further absence is recorded.
//
// The reset point is read from run_history inside the statement rather than
// supplied by the caller, so a work-observed run whose own history row could
// not be persisted still clears the absences before it.
func (s *Store) ResetHandoffAbsenceSequence(ctx context.Context, issueID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO handoff_absence_resets (issue_id, reset_run_id, updated_at)
		VALUES (?, COALESCE((SELECT MAX(id) FROM run_history WHERE issue_id = ?), 0), ?)
		ON CONFLICT (issue_id) DO UPDATE SET
			reset_run_id = excluded.reset_run_id,
			updated_at = excluded.updated_at`,
		issueID, issueID, now,
	)
	if err != nil {
		return fmt.Errorf("reset handoff absence sequence for %q: %w", issueID, err)
	}
	return nil
}

// QueryBudgetExhaustedIssues returns, for each candidate whose entry count
// meets or exceeds maxSessions, that count keyed by issue ID. Returns an
// empty non-nil map when none qualify or candidateIDs is empty.
func (s *Store) QueryBudgetExhaustedIssues(ctx context.Context, candidateIDs []string, maxSessions int) (map[string]int, error) {
	if len(candidateIDs) == 0 {
		return map[string]int{}, nil
	}

	placeholders := strings.Repeat("?,", len(candidateIDs))
	placeholders = placeholders[:len(placeholders)-1]

	args := make([]any, 0, len(candidateIDs)+1)
	for _, id := range candidateIDs {
		args = append(args, id)
	}
	args = append(args, maxSessions)

	query := fmt.Sprintf( //nolint:gosec // placeholders is "?,?,..." built from len(candidateIDs); no user data in format string
		`SELECT issue_id, COUNT(*) FROM run_history WHERE issue_id IN (%s) GROUP BY issue_id HAVING COUNT(*) >= ?`,
		placeholders,
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query budget exhausted issues: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	exhausted := map[string]int{}
	for rows.Next() {
		var issueID string
		var count int
		if err := rows.Scan(&issueID, &count); err != nil {
			return nil, fmt.Errorf("scan budget exhausted issue: %w", err)
		}
		exhausted[issueID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query budget exhausted issues: %w", err)
	}
	return exhausted, nil
}

// LatestAgentRuns returns, per issue ID, the run_history row with the
// greatest id among the issue's rows whose agent_adapter is non-empty. An
// issue with no such row is absent; an empty slice runs no query. Only the
// columns that describe the dispatch are filled.
func (s *Store) LatestAgentRuns(ctx context.Context, issueIDs []string) (map[string]RunHistory, error) {
	runs := map[string]RunHistory{}
	if len(issueIDs) == 0 {
		return runs, nil
	}

	placeholders := strings.Repeat("?,", len(issueIDs))
	placeholders = placeholders[:len(placeholders)-1]

	args := make([]any, 0, len(issueIDs))
	for _, id := range issueIDs {
		args = append(args, id)
	}

	query := fmt.Sprintf( //nolint:gosec // placeholders is "?,?,..." built from len(issueIDs); no user data in format string
		`SELECT id, issue_id, rule_name, agent_adapter, configured_model, stage_previous, stage_previous_outcome, chain_id, dispatched_at
		FROM run_history
		WHERE id IN (
			SELECT MAX(id) FROM run_history
			WHERE issue_id IN (%s) AND agent_adapter <> ''
			GROUP BY issue_id
		)`,
		placeholders,
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query latest agent runs: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	for rows.Next() {
		var run RunHistory
		if err := rows.Scan(&run.ID, &run.IssueID, &run.RuleName, &run.AgentAdapter, &run.ConfiguredModel,
			&run.StagePrevious, &run.StagePreviousOutcome, &run.ChainID, &run.DispatchedAt); err != nil {
			return nil, fmt.Errorf("scan latest agent run: %w", err)
		}
		runs[run.IssueID] = run
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query latest agent runs: %w", err)
	}
	return runs, nil
}

// IssueTokenUsage is the per-issue token spend read by the token ceiling
// and the cost_budget tool. The ByKind map makes the struct incomparable
// with ==; compare the scalar fields instead.
type IssueTokenUsage struct {
	TotalTokens        int64
	Sessions           int
	UnmeasuredSessions int

	// StoppedInFlight counts rows recording a session the token ceiling
	// stopped while running.
	StoppedInFlight int

	// UnaccountedTurns is the summed count of turns that spent tokens no
	// figure was proven to cover, so a non-zero value makes TotalTokens a
	// lower bound even when every row is measured.
	UnaccountedTurns int

	// InputTokens, OutputTokens, CacheReadTokens, and CacheWriteTokens sum
	// each raw class over the measured rows. Only [Store.TokenUsageByIssue]
	// fills them.
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64

	// ByKind holds the measured spend per agent kind in the classes cost
	// estimation prices. Only [Store.TokenUsageByIssue] fills it, and it is
	// nil when no kind spent anything.
	ByKind map[string]KindTokenUsage
}

// KindTokenUsage is one agent kind's measured run_history rows for an
// issue, in the token classes cost estimation prices.
type KindTokenUsage struct {
	// FreshInputTokens sums, per row, input_tokens less the cache counters
	// floored at zero. Flooring each row rather than the sum keeps the
	// figure equal to what pricing the rows one by one gives.
	FreshInputTokens int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	OutputTokens     int64
}

// SpendIncomplete reports whether TotalTokens is a lower bound rather than
// the whole of the issue's spend. Either count that leaves spend out of the
// sum answers true.
func (u IssueTokenUsage) SpendIncomplete() bool {
	return u.UnmeasuredSessions > 0 || u.UnaccountedTurns > 0
}

// TokenUsageByIssue returns the summed total_tokens, row count, unmeasured
// row count, and in-flight-stopped count across all rows for the issue, plus
// the measured spend per agent kind, or the zero [IssueTokenUsage] when the
// issue has no rows. An unmeasured row's token columns are zero, so a
// non-zero UnmeasuredSessions makes the total a lower bound. The totals and
// the per-kind breakdown come from one statement and describe the same rows.
// A kind whose measured rows spent nothing gets no ByKind entry.
func (s *Store) TokenUsageByIssue(ctx context.Context, issueID string) (IssueTokenUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT agent_adapter,
			COALESCE(SUM(total_tokens), 0),
			COUNT(*),
			SUM(CASE WHEN tokens_measured = 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN status = 'budget_stopped' THEN 1 ELSE 0 END),
			COALESCE(SUM(unaccounted_turns), 0),
			COALESCE(SUM(CASE WHEN tokens_measured = 1 THEN MAX(input_tokens - cache_read_tokens - cache_write_tokens, 0) ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN tokens_measured = 1 THEN cache_read_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN tokens_measured = 1 THEN cache_write_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN tokens_measured = 1 THEN output_tokens ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN tokens_measured = 1 THEN input_tokens ELSE 0 END), 0)
		FROM run_history WHERE issue_id = ? GROUP BY agent_adapter`, issueID,
	)
	if err != nil {
		return IssueTokenUsage{}, fmt.Errorf("token usage by issue %q: %w", issueID, err)
	}
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	var usage IssueTokenUsage
	for rows.Next() {
		var kind string
		var total int64
		var count int
		var unmeasured, stoppedInFlight, unaccounted sql.NullInt64
		var spent KindTokenUsage
		var rawInput int64
		if err := rows.Scan(&kind, &total, &count, &unmeasured, &stoppedInFlight, &unaccounted,
			&spent.FreshInputTokens, &spent.CacheReadTokens, &spent.CacheWriteTokens, &spent.OutputTokens, &rawInput); err != nil {
			return IssueTokenUsage{}, fmt.Errorf("token usage by issue %q: %w", issueID, err)
		}
		usage.TotalTokens += total
		usage.Sessions += count
		usage.UnmeasuredSessions += int(unmeasured.Int64)
		usage.StoppedInFlight += int(stoppedInFlight.Int64)
		usage.UnaccountedTurns += int(unaccounted.Int64)
		usage.InputTokens += rawInput
		usage.OutputTokens += spent.OutputTokens
		usage.CacheReadTokens += spent.CacheReadTokens
		usage.CacheWriteTokens += spent.CacheWriteTokens
		if spent == (KindTokenUsage{}) {
			continue
		}
		if usage.ByKind == nil {
			usage.ByKind = map[string]KindTokenUsage{}
		}
		usage.ByKind[kind] = spent
	}
	if err := rows.Err(); err != nil {
		return IssueTokenUsage{}, fmt.Errorf("token usage by issue %q: %w", issueID, err)
	}
	return usage, nil
}

// latestRunCompletionChunkSize caps identifiers per IN (...) query. The
// on-disk workspace directory count feeding this lookup is unbounded,
// unlike the tracker-page-bounded inputs of the other IN (...) queries
// here.
const latestRunCompletionChunkSize = 500

// LatestRunCompletionByIdentifier returns the most recent completed_at per
// identifier. Identifiers with no rows are omitted; an empty input returns
// an empty non-nil map without querying. Identifiers are queried in batches
// of at most [latestRunCompletionChunkSize] and merged.
func (s *Store) LatestRunCompletionByIdentifier(ctx context.Context, identifiers []string) (map[string]string, error) {
	result := make(map[string]string, len(identifiers))
	if len(identifiers) == 0 {
		return result, nil
	}

	for start := 0; start < len(identifiers); start += latestRunCompletionChunkSize {
		end := min(start+latestRunCompletionChunkSize, len(identifiers))
		chunk := identifiers[start:end]

		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]

		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}

		query := fmt.Sprintf( //nolint:gosec // placeholders is "?,?,..." built from len(chunk); no user data in format string
			`SELECT identifier, MAX(completed_at) FROM run_history WHERE identifier IN (%s) GROUP BY identifier`,
			placeholders,
		)

		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("query latest run completion by identifier: %w", err)
		}

		scanErr := func() error {
			defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable
			for rows.Next() {
				var identifier, completedAt string
				if err := rows.Scan(&identifier, &completedAt); err != nil {
					return fmt.Errorf("scan latest run completion by identifier: %w", err)
				}
				result[identifier] = completedAt
			}
			return rows.Err()
		}()
		if scanErr != nil {
			return nil, fmt.Errorf("query latest run completion by identifier: %w", scanErr)
		}
	}

	return result, nil
}

// QueryTokenBudgetUsage returns one [IssueTokenUsage] per candidate with at
// least one row; a candidate with no rows is absent, which the caller reads
// as zero on every count. An empty candidateIDs returns an empty non-nil map
// without querying. ByKind stays nil because no caller prices the result.
// Comparing against a ceiling is the caller's job.
func (s *Store) QueryTokenBudgetUsage(ctx context.Context, candidateIDs []string) (map[string]IssueTokenUsage, error) {
	usage := map[string]IssueTokenUsage{}
	if len(candidateIDs) == 0 {
		return usage, nil
	}

	placeholders := strings.Repeat("?,", len(candidateIDs))
	placeholders = placeholders[:len(placeholders)-1]

	args := make([]any, 0, len(candidateIDs))
	for _, id := range candidateIDs {
		args = append(args, id)
	}

	query := fmt.Sprintf( //nolint:gosec // placeholders is "?,?,..." built from len(candidateIDs); no user data in format string
		`SELECT issue_id, COALESCE(SUM(total_tokens), 0), COUNT(*), SUM(CASE WHEN tokens_measured = 0 THEN 1 ELSE 0 END),
		SUM(CASE WHEN status = 'budget_stopped' THEN 1 ELSE 0 END), COALESCE(SUM(unaccounted_turns), 0)
		FROM run_history WHERE issue_id IN (%s) GROUP BY issue_id`,
		placeholders,
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query token budget usage: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	for rows.Next() {
		var issueID string
		var issueUsage IssueTokenUsage
		var unmeasured, stoppedInFlight, unaccounted sql.NullInt64
		if err := rows.Scan(&issueID, &issueUsage.TotalTokens, &issueUsage.Sessions, &unmeasured, &stoppedInFlight, &unaccounted); err != nil {
			return nil, fmt.Errorf("scan token budget usage: %w", err)
		}
		issueUsage.UnmeasuredSessions = int(unmeasured.Int64)
		issueUsage.StoppedInFlight = int(stoppedInFlight.Int64)
		issueUsage.UnaccountedTurns = int(unaccounted.Int64)
		usage[issueID] = issueUsage
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query token budget usage: %w", err)
	}
	return usage, nil
}
