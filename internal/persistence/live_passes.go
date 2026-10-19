package persistence

import (
	"context"
	"fmt"
)

// Live pass kinds. A running stage session and a budget hold are stored; a
// queued retry is reported by [Store.ListLivePasses] from retry_entries.
const (
	LivePassRunning = "running" // a running stage session
	LivePassBudget  = "budget"  // a budget hold
	LivePassRetry   = "retry"   // a retry_entries row; ListLivePasses reports it, nothing stores it
)

// LivePass is a stage pass the orchestrator holds outside finished runs: the
// issue's chain and rule, and the rule whose hop reached it (empty when none
// did).
type LivePass struct {
	IssueID       string
	Kind          string
	ChainID       string
	RuleName      string
	StagePrevious string
}

// UpsertLivePass stores pass, replacing the row of its issue and kind.
func (s *Store) UpsertLivePass(ctx context.Context, pass LivePass) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO live_passes (issue_id, kind, chain_id, rule_name, stage_previous)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (issue_id, kind) DO UPDATE SET
			chain_id       = excluded.chain_id,
			rule_name      = excluded.rule_name,
			stage_previous = excluded.stage_previous`,
		pass.IssueID, pass.Kind, pass.ChainID, pass.RuleName, pass.StagePrevious)
	if err != nil {
		return fmt.Errorf("upsert live pass %q %q: %w", pass.IssueID, pass.Kind, err)
	}
	return nil
}

// DeleteLivePass removes the row of the issue and kind. It is a no-op, not an
// error, when no row exists.
func (s *Store) DeleteLivePass(ctx context.Context, issueID, kind string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM live_passes WHERE issue_id = ? AND kind = ?`, issueID, kind)
	if err != nil {
		return fmt.Errorf("delete live pass %q %q: %w", issueID, kind, err)
	}
	return nil
}

// DeleteAllLivePasses removes every live pass row.
func (s *Store) DeleteAllLivePasses(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM live_passes`); err != nil {
		return fmt.Errorf("delete all live passes: %w", err)
	}
	return nil
}

// ListLivePasses returns every live_passes row and, as kind [LivePassRetry],
// every retry_entries row, ordered by issue ID then kind. Both tables are
// read in one statement so the list is a single instant. It selects only the
// columns named here, so a read-only open of a database that a later
// migration extends still answers. Returns an empty slice (not nil) when none
// exist.
func (s *Store) ListLivePasses(ctx context.Context) ([]LivePass, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT issue_id, kind, chain_id, rule_name, stage_previous FROM live_passes
		UNION ALL
		SELECT issue_id, ?, chain_id, rule_name, stage_previous FROM retry_entries
		ORDER BY 1, 2`, LivePassRetry)
	if err != nil {
		return nil, fmt.Errorf("list live passes: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	passes := []LivePass{}
	for rows.Next() {
		var p LivePass
		if err := rows.Scan(&p.IssueID, &p.Kind, &p.ChainID, &p.RuleName, &p.StagePrevious); err != nil {
			return nil, fmt.Errorf("scan live pass: %w", err)
		}
		passes = append(passes, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list live passes: %w", err)
	}
	return passes, nil
}
