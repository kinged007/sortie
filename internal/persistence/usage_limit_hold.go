package persistence

import (
	"context"
	"fmt"
)

// UsageLimitHold is one persisted usage-limit hold.
type UsageLimitHold struct {
	AgentKind   string
	ModelOnly   bool   // Stored as 0 or 1.
	Model       string // Empty for a kind-wide hold and for the runtime's default model.
	ResetsAtMs  int64  // Unix epoch milliseconds.
	HeldSinceMs int64  // Unix epoch milliseconds.
}

// SaveUsageLimitHold persists a hold using upsert semantics. A hold with
// the same agent kind, model scope, and model is replaced entirely.
func (s *Store) SaveUsageLimitHold(ctx context.Context, hold UsageLimitHold) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage_limit_holds (agent_kind, model_only, model, resets_at_ms, held_since_ms)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (agent_kind, model_only, model) DO UPDATE SET
			resets_at_ms  = excluded.resets_at_ms,
			held_since_ms = excluded.held_since_ms`,
		hold.AgentKind, hold.ModelOnly, hold.Model, hold.ResetsAtMs, hold.HeldSinceMs,
	)
	if err != nil {
		return fmt.Errorf("save usage limit hold %q: %w", hold.AgentKind, err)
	}
	return nil
}

// LoadUsageLimitHolds returns every persisted hold ordered by agent kind,
// model scope, and model. Returns an empty slice (not nil) when none exist.
func (s *Store) LoadUsageLimitHolds(ctx context.Context) ([]UsageLimitHold, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_kind, model_only, model, resets_at_ms, held_since_ms
		FROM usage_limit_holds
		ORDER BY agent_kind ASC, model_only ASC, model ASC`)
	if err != nil {
		return nil, fmt.Errorf("load usage limit holds: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query; close error is non-actionable

	holds := []UsageLimitHold{}
	for rows.Next() {
		var h UsageLimitHold
		if err := rows.Scan(&h.AgentKind, &h.ModelOnly, &h.Model, &h.ResetsAtMs, &h.HeldSinceMs); err != nil {
			return nil, fmt.Errorf("scan usage limit hold: %w", err)
		}
		holds = append(holds, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load usage limit holds: %w", err)
	}
	return holds, nil
}

// DeleteUsageLimitHold removes the hold with the given key. It is a no-op,
// not an error, when no such hold exists.
func (s *Store) DeleteUsageLimitHold(ctx context.Context, agentKind string, modelOnly bool, model string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM usage_limit_holds WHERE agent_kind = ? AND model_only = ? AND model = ?`,
		agentKind, modelOnly, model)
	if err != nil {
		return fmt.Errorf("delete usage limit hold %q: %w", agentKind, err)
	}
	return nil
}
