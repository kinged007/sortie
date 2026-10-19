package persistence

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Activity values of a reaction_activity row.
const (
	ReactionActivityWatch        = "watch"
	ReactionActivityContinuation = "continuation"
	ReactionActivityAction       = "action"
	ReactionActivityEscalation   = "escalation"
)

// ReactionActivity is one thing a pull request reaction did in one pass of an
// issue.
type ReactionActivity struct {
	IssueID    string
	ChainID    string
	Kind       string
	Activity   string
	RecordedAt time.Time
}

// PassRef names one pass of one issue and when its first run was dispatched.
type PassRef struct {
	IssueID      string
	ChainID      string
	DispatchedAt time.Time // zero for an undated pass
}

// PassKey keys a pass in a PassActivities result.
type PassKey struct {
	IssueID string
	ChainID string
}

// KindActivity sums what one reaction kind recorded in one pass.
type KindActivity struct {
	Watched       bool
	Continuations int
	Acted         bool // an action row exists
	Escalated     bool
}

// PassActivity is what the reactions recorded in one pass. Recorded is false
// for a pass without a chain identifier or a dispatch time, or dispatched
// before the second the table was created in; Kinds is then empty.
type PassActivity struct {
	Recorded bool
	Kinds    map[string]KindActivity // keyed by reaction kind
}

// RecordReactionActivity appends records in one transaction. A watch record
// whose issue, chain, and kind already hold one is ignored. An empty list
// writes nothing.
func (s *Store) RecordReactionActivity(ctx context.Context, records []ReactionActivity) error {
	if len(records) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin record reaction activity: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback on error path; no-op after commit

	for _, r := range records {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO reaction_activity (issue_id, chain_id, kind, activity, recorded_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`,
			r.IssueID, r.ChainID, r.Kind, r.Activity, r.RecordedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record reaction activity %q of %q: %w", r.Activity, r.IssueID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit record reaction activity: %w", err)
	}
	return nil
}

// PassActivities returns what each listed pass recorded, keyed by issue and
// chain. A listed pass without rows maps to its Recorded value and empty
// Kinds. An empty list reads nothing and returns an empty, non-nil map.
func (s *Store) PassActivities(ctx context.Context, passes []PassRef) (map[PassKey]PassActivity, error) {
	result := make(map[PassKey]PassActivity, len(passes))
	if len(passes) == 0 {
		return result, nil
	}

	// The row query below holds the store's only connection until it is
	// closed, so this read must finish first.
	trackedSince, err := s.reactionActivityTrackedSince(ctx)
	if err != nil {
		return nil, err
	}

	recorded := make(map[PassKey]struct{}, len(passes))
	issueSet := make(map[string]struct{}, len(passes))
	for _, p := range passes {
		key := PassKey{IssueID: p.IssueID, ChainID: p.ChainID}
		if _, ok := recorded[key]; ok {
			continue
		}
		isRecorded := p.ChainID != "" && !p.DispatchedAt.IsZero() &&
			!p.DispatchedAt.Truncate(time.Second).Before(trackedSince)
		result[key] = PassActivity{Recorded: isRecorded}
		if isRecorded {
			recorded[key] = struct{}{}
			issueSet[p.IssueID] = struct{}{}
		}
	}
	if len(recorded) == 0 {
		return result, nil
	}

	args := make([]any, 0, len(issueSet))
	for id := range issueSet {
		args = append(args, id)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")

	query := fmt.Sprintf( //nolint:gosec // placeholders is built only from len(args); values remain bound parameters
		`SELECT issue_id, chain_id, kind, activity, COUNT(*)
		FROM reaction_activity
		WHERE issue_id IN (%s)
		GROUP BY issue_id, chain_id, kind, activity`,
		placeholders,
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query reaction activity: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	for rows.Next() {
		var key PassKey
		var kind, activity string
		var count int
		if err := rows.Scan(&key.IssueID, &key.ChainID, &kind, &activity, &count); err != nil {
			return nil, fmt.Errorf("scan reaction activity: %w", err)
		}
		if _, ok := recorded[key]; !ok {
			continue
		}

		pass := result[key]
		if pass.Kinds == nil {
			pass.Kinds = make(map[string]KindActivity)
		}
		ka := pass.Kinds[kind]
		switch activity {
		case ReactionActivityWatch:
			ka.Watched = true
		case ReactionActivityContinuation:
			ka.Continuations += count
		case ReactionActivityAction:
			ka.Acted = true
		case ReactionActivityEscalation:
			ka.Escalated = true
		default:
			continue
		}
		pass.Kinds[kind] = ka
		result[key] = pass
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query reaction activity: %w", err)
	}
	return result, nil
}

// reactionActivityTrackedSince returns the time the migration creating
// reaction_activity was applied, in whole seconds.
func (s *Store) reactionActivityTrackedSince(ctx context.Context) (time.Time, error) {
	var appliedAt string
	if err := s.db.QueryRowContext(ctx,
		`SELECT applied_at FROM schema_migrations WHERE version = ?`,
		reactionActivityMigration,
	).Scan(&appliedAt); err != nil {
		return time.Time{}, fmt.Errorf("read reaction activity start: %w", err)
	}
	t, err := time.Parse(time.RFC3339, appliedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse reaction activity start %q: %w", appliedAt, err)
	}
	return t, nil
}
