package persistence

import (
	"context"
	"maps"
	"testing"
	"time"
)

func TestReactionActivity(t *testing.T) {
	t.Parallel()

	rec := func(chain, kind, activity string) ReactionActivity {
		return ReactionActivity{IssueID: "ISS-1", ChainID: chain, Kind: kind, Activity: activity}
	}
	all := []ReactionActivity{
		rec("c1", "ci", ReactionActivityWatch),
		rec("c1", "ci", ReactionActivityWatch),
		rec("c1", "ci", ReactionActivityContinuation),
		rec("c1", "ci", ReactionActivityContinuation),
		rec("c1", "ci", ReactionActivityAction),
		rec("c1", "review", ReactionActivityEscalation),
		rec("c0", "ci", ReactionActivityAction),
		rec("", "ci", ReactionActivityAction),
	}

	tests := []struct {
		name     string
		chain    string
		undated  bool
		offset   time.Duration
		records  []ReactionActivity
		wantRows int
		want     PassActivity
	}{
		{name: "no chain id", chain: "", records: all, wantRows: 7},
		{name: "no dispatch time", chain: "c1", undated: true, records: all, wantRows: 7},
		{name: "dispatched before applied_at", chain: "c1", offset: -time.Nanosecond, records: all, wantRows: 7},
		{name: "no rows", chain: "c1", want: PassActivity{Recorded: true}},
		{
			name: "dispatched in the applied_at second", chain: "c1", offset: 999 * time.Millisecond,
			records: all, wantRows: 7,
			want: PassActivity{Recorded: true, Kinds: map[string]KindActivity{
				"ci":     {Watched: true, Continuations: 2, Acted: true},
				"review": {Escalated: true},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			s := mustOpenStore(t)
			var appliedAt string
			if err := s.db.QueryRowContext(ctx, `SELECT applied_at FROM schema_migrations WHERE version = ?`, reactionActivityMigration).Scan(&appliedAt); err != nil {
				t.Fatalf("read applied_at: %v", err)
			}
			applied, err := time.Parse(time.RFC3339, appliedAt)
			if err != nil {
				t.Fatalf("parse applied_at %q: %v", appliedAt, err)
			}
			pass := PassRef{IssueID: "ISS-1", ChainID: tt.chain}
			if !tt.undated {
				pass.DispatchedAt = applied.Add(tt.offset)
			}

			if err := s.RecordReactionActivity(ctx, tt.records); err != nil {
				t.Fatalf("RecordReactionActivity: %v", err)
			}
			var rows int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reaction_activity`).Scan(&rows); err != nil {
				t.Fatalf("count rows: %v", err)
			}
			got, err := s.PassActivities(ctx, []PassRef{pass})
			if err != nil {
				t.Fatalf("PassActivities(%+v): %v", pass, err)
			}

			if rows != tt.wantRows {
				t.Errorf("reaction_activity rows = %d, want %d", rows, tt.wantRows)
			}
			gotPass, ok := got[PassKey{IssueID: pass.IssueID, ChainID: pass.ChainID}]
			if !ok || gotPass.Recorded != tt.want.Recorded || !maps.Equal(gotPass.Kinds, tt.want.Kinds) {
				t.Errorf("PassActivities(%+v) = %+v (listed %t), want %+v", pass, gotPass, ok, tt.want)
			}
		})
	}
}
