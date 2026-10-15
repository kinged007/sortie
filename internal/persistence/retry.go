package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// RetryEntry represents a persisted pending retry for a single issue.
// The timer_handle field from the domain model is runtime-only and is not
// included in the persisted representation.
type RetryEntry struct {
	IssueID    string  // Tracker-internal issue ID (primary key).
	Identifier string  // Human-readable ticket key (best-effort, for logs).
	Attempt    int     // Retry attempt number, 1-based.
	DueAtMs    int64   // Unix epoch milliseconds when the retry timer should fire.
	Error      *string // Last error message; nil when no error.
	SessionID  *string // Adapter-assigned session identifier from the previous worker attempt. Nil when no session was established.
	RuleName   string  // Dispatch rule name; empty for legacy rows and fallback dispatches.
	TemplateID string  // Resolved template path; empty selects the WORKFLOW.md body template.
	AgentKind  string  // Agent adapter kind; empty for legacy rows.

	// StagePrevious and StagePreviousOutcome are the frozen stage.previous
	// pair of the dispatch this retry continues; both are empty when no hop
	// led to it and for legacy rows.
	StagePrevious        string
	StagePreviousOutcome string

	// ChainID is the chain frozen with the retry's selection; empty for
	// legacy rows.
	ChainID string

	// DisplayID is the qualified issue form (e.g. "owner/repo#9"); IssueTitle
	// and IssueURL are the tracker's title and web link as read when the
	// retry was scheduled. Each is empty for legacy rows and for a retry
	// that holds no read of its issue.
	DisplayID  string
	IssueTitle string
	IssueURL   string

	// Trigger is the sortie_retries_total label of the schedule that
	// created the retry; empty for legacy rows and for a schedule that
	// records none.
	Trigger string

	// OriginTrigger is what started the retry, kept through every
	// put-back; empty when the retry never stored it, and for legacy rows.
	OriginTrigger string

	// ConfiguredModel is the model the retried dispatch was configured
	// with; empty when unset and for legacy rows.
	ConfiguredModel string

	// IssueType, IssueLabels, and IssueState are the tracker's facts as
	// read when the retry was scheduled. IssueLabels is nil when the read
	// recorded none (stored as an empty string) and an empty non-nil slice
	// when the read saw an issue without labels.
	IssueType   string
	IssueLabels []string
	IssueState  string

	// DispatchedAt is the RFC 3339 start of the first run of the pass this
	// retry belongs to; empty when the pass could not be dated.
	DispatchedAt string
}

// PendingRetry pairs a persisted [RetryEntry] with the computed delay
// remaining until its timer should fire. RemainingMs is zero when the entry's
// due time has already passed, meaning the retry should fire immediately on
// startup.
type PendingRetry struct {
	Entry       RetryEntry
	RemainingMs int64 // max(entry.DueAtMs - nowMs, 0); always >= 0
}

// SaveRetryEntry persists a retry entry using upsert semantics. If an entry
// with the same IssueID already exists, it is replaced entirely.
func (s *Store) SaveRetryEntry(ctx context.Context, entry RetryEntry) error {
	errVal := sql.NullString{}
	if entry.Error != nil {
		errVal = sql.NullString{String: *entry.Error, Valid: true}
	}

	ssnVal := sql.NullString{}
	if entry.SessionID != nil {
		ssnVal = sql.NullString{String: *entry.SessionID, Valid: true}
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO retry_entries (issue_id, identifier, attempt, due_at_ms, error, session_id, rule_name, template_id, agent_kind, stage_previous, stage_previous_outcome, chain_id, display_identifier, issue_title, issue_url, trigger, configured_model, issue_type, issue_labels, issue_state, dispatched_at, origin_trigger)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET
			identifier             = excluded.identifier,
			attempt                = excluded.attempt,
			due_at_ms              = excluded.due_at_ms,
			error                  = excluded.error,
			session_id             = excluded.session_id,
			rule_name              = excluded.rule_name,
			template_id            = excluded.template_id,
			agent_kind             = excluded.agent_kind,
			stage_previous         = excluded.stage_previous,
			stage_previous_outcome = excluded.stage_previous_outcome,
			chain_id               = excluded.chain_id,
			display_identifier     = excluded.display_identifier,
			issue_title            = excluded.issue_title,
			issue_url              = excluded.issue_url,
			trigger                = excluded.trigger,
			configured_model       = excluded.configured_model,
			issue_type             = excluded.issue_type,
			issue_labels           = excluded.issue_labels,
			issue_state            = excluded.issue_state,
			dispatched_at          = excluded.dispatched_at,
			origin_trigger         = excluded.origin_trigger`,
		entry.IssueID, entry.Identifier, entry.Attempt, entry.DueAtMs, errVal, ssnVal,
		entry.RuleName, entry.TemplateID, entry.AgentKind,
		entry.StagePrevious, entry.StagePreviousOutcome, entry.ChainID,
		entry.DisplayID, entry.IssueTitle, entry.IssueURL, entry.Trigger,
		entry.ConfiguredModel, entry.IssueType, encodeIssueLabels(entry.IssueLabels), entry.IssueState, entry.DispatchedAt,
		entry.OriginTrigger,
	)
	if err != nil {
		return fmt.Errorf("save retry entry %q: %w", entry.IssueID, err)
	}
	return nil
}

// LoadRetryEntries returns all persisted retry entries ordered by due_at_ms
// ascending for deterministic startup reconstruction. Returns an empty slice
// (not nil) when no entries exist.
func (s *Store) LoadRetryEntries(ctx context.Context) ([]RetryEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT issue_id, identifier, attempt, due_at_ms, error, session_id, rule_name, template_id, agent_kind,
			stage_previous, stage_previous_outcome, chain_id, display_identifier, issue_title, issue_url, trigger,
			configured_model, issue_type, issue_labels, issue_state, dispatched_at, origin_trigger
		FROM retry_entries
		ORDER BY due_at_ms ASC, issue_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("load retry entries: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	entries := []RetryEntry{}
	for rows.Next() {
		var e RetryEntry
		var errVal sql.NullString
		var ssnVal sql.NullString
		var labelsVal string
		if err := rows.Scan(&e.IssueID, &e.Identifier, &e.Attempt, &e.DueAtMs, &errVal, &ssnVal,
			&e.RuleName, &e.TemplateID, &e.AgentKind, &e.StagePrevious, &e.StagePreviousOutcome, &e.ChainID,
			&e.DisplayID, &e.IssueTitle, &e.IssueURL, &e.Trigger,
			&e.ConfiguredModel, &e.IssueType, &labelsVal, &e.IssueState, &e.DispatchedAt, &e.OriginTrigger); err != nil {
			return nil, fmt.Errorf("scan retry entry: %w", err)
		}
		e.IssueLabels = decodeIssueLabels(labelsVal)
		if errVal.Valid {
			e.Error = new(errVal.String)
		}
		if ssnVal.Valid {
			e.SessionID = new(ssnVal.String)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load retry entries: %w", err)
	}
	return entries, nil
}

// encodeIssueLabels stores nil as the empty string so "no read" stays
// distinct from the JSON array "[]" of a read that saw no labels.
func encodeIssueLabels(labels []string) string {
	if labels == nil {
		return ""
	}
	encoded, err := json.Marshal(labels)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// decodeIssueLabels returns nil for the empty string and for any value that
// is not a JSON array of strings.
func decodeIssueLabels(encoded string) []string {
	if encoded == "" {
		return nil
	}
	var labels []string
	if err := json.Unmarshal([]byte(encoded), &labels); err != nil {
		return nil
	}
	return labels
}

// DeleteRetryEntry removes the retry entry for the given issue ID. It is a
// no-op if no entry exists for that issue ID.
func (s *Store) DeleteRetryEntry(ctx context.Context, issueID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM retry_entries WHERE issue_id = ?`, issueID)
	if err != nil {
		return fmt.Errorf("delete retry entry %q: %w", issueID, err)
	}
	return nil
}

// LoadRetryEntriesForRecovery loads all persisted retry entries and computes
// the remaining delay for each relative to nowMs (Unix epoch milliseconds).
// Entries whose due_at_ms has already passed get RemainingMs = 0, meaning the
// retry should fire immediately. Results are ordered by due_at_ms ascending
// then issue_id ascending (same order as [Store.LoadRetryEntries]).
func (s *Store) LoadRetryEntriesForRecovery(ctx context.Context, nowMs int64) ([]PendingRetry, error) {
	entries, err := s.LoadRetryEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("load retry entries for recovery: %w", err)
	}

	pending := make([]PendingRetry, len(entries))
	for i, e := range entries {
		remaining := max(e.DueAtMs-nowMs, 0)
		pending[i] = PendingRetry{Entry: e, RemainingMs: remaining}
	}
	return pending, nil
}
