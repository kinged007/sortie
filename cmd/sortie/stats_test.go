package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/persistence"
	"github.com/sortie-ai/sortie/internal/runstats"
	"github.com/sortie-ai/sortie/internal/server"
)

var fullCaps = persistence.RunHistoryCapabilities{
	HasTurnsCompleted:   true,
	HasReviewMetadata:   true,
	HasRuleRouting:      true,
	HasTokens:           true,
	HasTokenMeasurement: true,
}

var fixedNow = time.Date(2026, 8, 7, 9, 15, 0, 0, time.UTC)

func addRows(t *testing.T, agg *runstats.Aggregator, rows ...persistence.RunStatsRow) {
	t.Helper()
	for _, r := range rows {
		if err := agg.Add(r); err != nil {
			t.Fatalf("Aggregator.Add(%+v): %v", r, err)
		}
	}
}

func findGroup(t *testing.T, groups []runstats.Group, name string) runstats.Group {
	t.Helper()
	for _, g := range groups {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("group %q not found in %+v", name, groups)
	return runstats.Group{}
}

// statsWorkflow returns a minimal WORKFLOW.md whose front matter is
// extended with extra, for stats tests that need a specific top-level
// key such as db_path without a full tracker/agent configuration.
func statsWorkflow(extra string) []byte {
	return fmt.Appendf(nil, `---
tracker:
  kind: file
  active_states:
    - To Do
  terminal_states:
    - Done
agent:
  kind: mock
%s---
Do {{ .issue.title }}.
`, extra)
}

// createStatsDB creates a fully migrated SQLite database at dbPath and
// appends each of runs, then closes the store so a later read-only open
// sees committed data.
func createStatsDB(t *testing.T, dbPath string, runs ...persistence.RunHistory) {
	t.Helper()
	ctx := context.Background()
	store, err := persistence.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("persistence.Open(%q): %v", dbPath, err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, r := range runs {
		if _, err := store.AppendRunHistory(ctx, r); err != nil {
			t.Fatalf("AppendRunHistory: %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// createLegacyStatsDB creates dbPath with only the migration-001
// run_history columns (no schema_migrations tracking) and inserts one
// row, matching a database written by a pre-migration-010 binary. The
// modernc.org/sqlite driver is already registered process-wide via
// internal/persistence's import.
func createLegacyStatsDB(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open(%q): %v", dbPath, err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close: %v", err)
		}
	})

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE run_history (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		issue_id      TEXT    NOT NULL,
		identifier    TEXT    NOT NULL,
		attempt       INTEGER NOT NULL,
		agent_adapter TEXT    NOT NULL,
		workspace     TEXT    NOT NULL,
		started_at    TEXT    NOT NULL,
		completed_at  TEXT    NOT NULL,
		status        TEXT    NOT NULL,
		error         TEXT
	)`); err != nil {
		t.Fatalf("create migration-001 run_history table: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO run_history (issue_id, identifier, attempt, agent_adapter, workspace, started_at, completed_at, status)
		 VALUES ('ISS-1', 'PROJ-1', 1, 'mock', '/tmp/ws', '2026-01-01T00:00:00Z', '2026-01-01T00:10:00Z', 'succeeded')`,
	); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
}

// createFullSchemaWithoutCacheWriteTokensDB builds dbPath with every
// optional run_history column the full schema tier requires, except
// cache_write_tokens, matching a database migration 020 has not yet
// reached, and inserts one measured row with the given rate-relevant
// token counts.
func createFullSchemaWithoutCacheWriteTokensDB(t *testing.T, dbPath string, inputTokens, outputTokens, cacheReadTokens int64) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open(%q): %v", dbPath, err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close: %v", err)
		}
	})

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE run_history (
		id                  INTEGER PRIMARY KEY AUTOINCREMENT,
		issue_id            TEXT    NOT NULL,
		identifier          TEXT    NOT NULL,
		attempt             INTEGER NOT NULL,
		agent_adapter       TEXT    NOT NULL,
		workspace           TEXT    NOT NULL,
		started_at          TEXT    NOT NULL,
		completed_at        TEXT    NOT NULL,
		status              TEXT    NOT NULL,
		error               TEXT,
		workflow_file       TEXT,
		turns_completed     INTEGER NOT NULL DEFAULT 0,
		display_identifier  TEXT,
		review_metadata     TEXT,
		rule_name           TEXT    NOT NULL DEFAULT '',
		template_id         TEXT    NOT NULL DEFAULT '',
		input_tokens        INTEGER NOT NULL DEFAULT 0,
		output_tokens       INTEGER NOT NULL DEFAULT 0,
		total_tokens        INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens   INTEGER NOT NULL DEFAULT 0,
		tokens_measured     INTEGER NOT NULL DEFAULT 1,
		unaccounted_turns   INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("create pre-migration-020 run_history table: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO run_history (issue_id, identifier, attempt, agent_adapter, workspace, started_at, completed_at,
			status, turns_completed, input_tokens, output_tokens, total_tokens, cache_read_tokens, tokens_measured)
		 VALUES ('ISS-CW', 'PROJ-CW', 1, 'claude-code', '/tmp/ws', '2026-01-01T00:00:00Z', '2026-01-01T00:10:00Z',
			'succeeded', 1, ?, ?, ?, ?, 1)`,
		inputTokens, outputTokens, inputTokens+outputTokens, cacheReadTokens,
	); err != nil {
		t.Fatalf("insert pre-migration-020 row: %v", err)
	}
}

// newStatsWorkspace creates a temp directory holding a minimal
// WORKFLOW.md and a database built by build, and returns the workflow
// path.
//
// Every caller gets its own directory, so parallel subtests never share
// one database file. Two read-only opens of the same file race to recover
// its write-ahead log, and the loser gets SQLITE_BUSY; on Linux the race
// is almost always won in time, on Windows it is not.
func newStatsWorkspace(t *testing.T, build func(t *testing.T, dbPath string)) string {
	t.Helper()
	dir := t.TempDir()
	wfPath := writeCustomWorkflowFile(t, dir, statsWorkflow(""))
	build(t, filepath.Join(dir, ".sortie.db"))
	return wfPath
}

// pinStatsNow overrides the package-level statsNow for the life of the
// calling test, following the serverShutdownTimeout precedent. A test
// that calls this must not also call t.Parallel().
func pinStatsNow(t *testing.T, now time.Time) {
	t.Helper()
	orig := statsNow
	statsNow = func() time.Time { return now }
	t.Cleanup(func() { statsNow = orig })
}

func TestStatsCostDerivation(t *testing.T) {
	t.Parallel()

	makeRow := func(adapter string, input int64) persistence.RunStatsRow {
		return persistence.RunStatsRow{
			Status: "succeeded", AgentAdapter: adapter,
			StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z",
			InputTokens: input, TokensMeasured: true,
		}
	}

	t.Run("every adapter priced", func(t *testing.T) {
		t.Parallel()

		rates := server.TokenRates{
			"claude-code": server.TokenRateConfig{InputPerMtok: new(float64(10)), OutputPerMtok: new(float64(0))},
			"codex":       server.TokenRateConfig{InputPerMtok: new(float64(20)), OutputPerMtok: new(float64(0))},
		}
		agg := runstats.New(fullCaps, statsPricer(rates))
		addRows(t, agg, makeRow("claude-code", 1_000_000), makeRow("codex", 1_000_000))

		report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

		if report.Summary.CostUnpricedRuns != 0 {
			t.Errorf("Summary.CostUnpricedRuns = %d, want 0", report.Summary.CostUnpricedRuns)
		}
		if report.Summary.CostUSD == nil || *report.Summary.CostUSD != 30 {
			t.Fatalf("Summary.CostUSD = %v, want 30", report.Summary.CostUSD)
		}
		if footnotes := statsFootnotes(report.Summary, 0); len(footnotes) != 0 {
			t.Errorf("statsFootnotes = %v, want none", footnotes)
		}
	})

	t.Run("one adapter priced, one not", func(t *testing.T) {
		t.Parallel()

		rates := server.TokenRates{
			"claude-code": server.TokenRateConfig{InputPerMtok: new(float64(10)), OutputPerMtok: new(float64(0))},
		}
		agg := runstats.New(fullCaps, statsPricer(rates))
		addRows(t, agg, makeRow("claude-code", 1_000_000), makeRow("codex", 1_000_000))

		report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

		if report.Summary.CostUnpricedRuns != 1 {
			t.Errorf("Summary.CostUnpricedRuns = %d, want 1", report.Summary.CostUnpricedRuns)
		}
		if report.Summary.CostUSD == nil || *report.Summary.CostUSD != 10 {
			t.Fatalf("Summary.CostUSD = %v, want 10 (priced row only)", report.Summary.CostUSD)
		}
		footnotes := statsFootnotes(report.Summary, 0)
		found := slices.ContainsFunc(footnotes, func(f string) bool {
			return strings.Contains(f, "the cost figures skip 1 of these runs")
		})
		if !found {
			t.Errorf("statsFootnotes = %v, want an unpriced-run footnote", footnotes)
		}
	})

	t.Run("the only entry is incomplete: dash and cost-unpriced footnote, not the token_rates remedy", func(t *testing.T) {
		t.Parallel()

		rates := server.TokenRates{
			"claude-code": server.TokenRateConfig{OutputPerMtok: new(float64(15))},
		}
		agg := runstats.New(fullCaps, statsPricer(rates))
		addRows(t, agg, makeRow("claude-code", 1_000_000))

		report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

		if report.Summary.CostUnpricedRuns != 1 {
			t.Errorf("Summary.CostUnpricedRuns = %d, want 1", report.Summary.CostUnpricedRuns)
		}
		if report.Summary.CostUSD != nil {
			t.Errorf("Summary.CostUSD = %v, want nil (the only entry prices nothing)", *report.Summary.CostUSD)
		}

		footnotes := statsFootnotes(report.Summary, 0)
		found := slices.ContainsFunc(footnotes, func(f string) bool {
			return strings.Contains(f, "the cost figures skip 1 of these runs")
		})
		if !found {
			t.Errorf("statsFootnotes = %v, want an unpriced-run footnote", footnotes)
		}

		out := renderText(report)
		if !strings.Contains(out, "cost (measured runs)    -   per succeeded run -") {
			t.Errorf("renderStatsText stdout = %q, want the dash arm", out)
		}
		if strings.Contains(out, "not estimated; set token_rates") {
			t.Errorf("renderStatsText stdout = %q, want no token_rates remedy naming a cause that is not the actual one", out)
		}
	})

	t.Run("no token_rates configured at all", func(t *testing.T) {
		t.Parallel()

		agg := runstats.New(fullCaps, nil)
		addRows(t, agg, makeRow("claude-code", 1_000_000), makeRow("codex", 1_000_000))

		report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

		if report.Summary.CostUSD != nil {
			t.Errorf("Summary.CostUSD = %v, want nil", *report.Summary.CostUSD)
		}
		if report.Summary.CostPerSucceededRunUSD != nil {
			t.Errorf("Summary.CostPerSucceededRunUSD = %v, want nil", *report.Summary.CostPerSucceededRunUSD)
		}
		if report.Summary.CostUnpricedRuns != 0 {
			t.Errorf("Summary.CostUnpricedRuns = %d, want 0 even though the range holds rows", report.Summary.CostUnpricedRuns)
		}
		if footnotes := statsFootnotes(report.Summary, 0); len(footnotes) != 0 {
			t.Errorf("statsFootnotes = %v, want none (the cost column is already dropped)", footnotes)
		}

		out := renderText(report)
		if !strings.Contains(out, "not estimated; set token_rates in the workflow") {
			t.Errorf("renderStatsText stdout = %q, want the not-estimated line", out)
		}
		if strings.Contains(out, "the cost figures skip") {
			t.Errorf("renderStatsText stdout = %q, want no unpriced disclosure", out)
		}
	})

	t.Run("all runs unmeasured with token_rates configured renders a dash for both figures", func(t *testing.T) {
		t.Parallel()

		rates := server.TokenRates{"kiro": server.TokenRateConfig{InputPerMtok: new(float64(10)), OutputPerMtok: new(float64(0))}}
		agg := runstats.New(fullCaps, statsPricer(rates))
		addRows(t, agg, persistence.RunStatsRow{
			Status: "succeeded", AgentAdapter: "kiro",
			StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z",
			TokensMeasured: false,
		})

		report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)
		if report.Summary.CostUSD != nil {
			t.Errorf("Summary.CostUSD = %v, want nil (no measured run in range)", *report.Summary.CostUSD)
		}

		out := renderText(report)
		if !strings.Contains(out, "cost (measured runs)    -   per succeeded run -") {
			t.Errorf("renderStatsText stdout = %q, want the dash arm, not the token_rates remedy", out)
		}
		if strings.Contains(out, "not estimated; set token_rates") {
			t.Errorf("renderStatsText stdout = %q, want no token_rates remedy naming a cause that is not the actual one", out)
		}
	})

	t.Run("a range of only ci_failed rows under a complete rate entry renders the dash line without a skip note", func(t *testing.T) {
		t.Parallel()

		rates := server.TokenRates{"claude-code": server.TokenRateConfig{InputPerMtok: new(float64(10)), OutputPerMtok: new(float64(0))}}
		agg := runstats.New(fullCaps, statsPricer(rates))
		addRows(t, agg, persistence.RunStatsRow{
			Status:    "ci_failed",
			StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:00:00Z",
			TokensMeasured: true,
		})

		out := renderText(agg.Report(fixedNow, "/wf", "/db", nil, nil, nil))

		if !strings.Contains(out, "cost (measured runs)    -   per succeeded run -") {
			t.Errorf("renderStatsText stdout = %q, want the dash arm", out)
		}
		if strings.Contains(out, "not estimated; set token_rates") {
			t.Errorf("renderStatsText stdout = %q, want no token_rates remedy under configured rates", out)
		}
		if strings.Contains(out, "the cost figures skip") {
			t.Errorf("renderStatsText stdout = %q, want no unpriced-run note for a ci_failed row", out)
		}
	})

	t.Run("a mixed range with no rates configured still names token_rates as the remedy", func(t *testing.T) {
		t.Parallel()

		agg := runstats.New(fullCaps, nil)
		addRows(t, agg,
			makeRow("claude-code", 1_000_000),
			persistence.RunStatsRow{
				Status: "succeeded", AgentAdapter: "kiro",
				StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z",
				TokensMeasured: false,
			},
		)

		report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

		out := renderText(report)
		if !strings.Contains(out, "not estimated; set token_rates in the workflow") {
			t.Errorf("renderStatsText stdout = %q, want the token_rates remedy for a range with a measured run", out)
		}
	})

	t.Run("a range with a measured priced run renders numbers", func(t *testing.T) {
		t.Parallel()

		rates := server.TokenRates{"claude-code": server.TokenRateConfig{InputPerMtok: new(float64(10)), OutputPerMtok: new(float64(0))}}
		agg := runstats.New(fullCaps, statsPricer(rates))
		addRows(t, agg, makeRow("claude-code", 1_000_000))

		report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

		out := renderText(report)
		if !strings.Contains(out, "cost (measured runs)    $10.00   per succeeded run $10.00") {
			t.Errorf("renderStatsText stdout = %q, want the priced numbers line", out)
		}
	})
}

func TestStatsTokenReporting(t *testing.T) {
	t.Parallel()

	t.Run("stored total_tokens is reported unchanged", func(t *testing.T) {
		t.Parallel()

		agg := runstats.New(fullCaps, nil)
		addRows(t, agg, persistence.RunStatsRow{
			Status: "succeeded", AgentAdapter: "mock",
			StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z",
			InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10,
			TotalTokens:    999, // deliberately inconsistent with input+output+cache_read = 160
			TokensMeasured: true,
		})

		report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

		if report.Summary.Tokens == nil || report.Summary.Tokens.Total != 999 {
			t.Errorf("Summary.Tokens.Total = %v, want 999 (stored total, never recomputed)", report.Summary.Tokens)
		}
		adapterGroup := findGroup(t, report.ByAdapter, "mock")
		if adapterGroup.Tokens == nil || adapterGroup.Tokens.Total != 999 {
			t.Errorf("by_adapter[mock].Tokens.Total = %v, want 999", adapterGroup.Tokens)
		}
	})

	measuredUnmeasuredRows := func() []persistence.RunStatsRow {
		return []persistence.RunStatsRow{
			{
				Status: "succeeded", AgentAdapter: "claude-code",
				StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z",
				InputTokens: 1_000_000, TotalTokens: 1_000_000, TokensMeasured: true,
			},
			{
				Status: "succeeded", AgentAdapter: "claude-code",
				StartedAt: "2026-01-01T01:00:00Z", CompletedAt: "2026-01-01T01:10:00Z",
				TokensMeasured: true, // a measurement of zero
			},
			{
				Status: "succeeded", AgentAdapter: "kiro",
				StartedAt: "2026-01-01T02:00:00Z", CompletedAt: "2026-01-01T02:10:00Z",
				TokensMeasured: false,
			},
		}
	}

	t.Run("JSON form folds a measured, measured-zero, and unmeasured run", func(t *testing.T) {
		t.Parallel()

		rates := server.TokenRates{
			"claude-code": server.TokenRateConfig{InputPerMtok: new(float64(10)), OutputPerMtok: new(float64(0))},
			"kiro":        server.TokenRateConfig{InputPerMtok: new(float64(10)), OutputPerMtok: new(float64(0))},
		}
		agg := runstats.New(fullCaps, statsPricer(rates))
		addRows(t, agg, measuredUnmeasuredRows()...)

		report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

		if report.Summary.Tokens == nil || report.Summary.Tokens.Total != 1_000_000 {
			t.Errorf("Summary.Tokens.Total = %v, want 1000000 (measured runs only)", report.Summary.Tokens)
		}
		if report.Summary.TokensUnmeasuredRuns != 1 {
			t.Errorf("Summary.TokensUnmeasuredRuns = %d, want 1", report.Summary.TokensUnmeasuredRuns)
		}
		if report.Summary.CostUSD == nil || *report.Summary.CostUSD != 10 {
			t.Fatalf("Summary.CostUSD = %v, want 10 (prices only the measured claude-code runs)", report.Summary.CostUSD)
		}
		if report.Summary.CostPerSucceededRunUSD == nil || *report.Summary.CostPerSucceededRunUSD != 5 {
			t.Errorf("Summary.CostPerSucceededRunUSD = %v, want 5 (10 / 2 succeeded measured runs)", report.Summary.CostPerSucceededRunUSD)
		}

		unmeasuredGroup := findGroup(t, report.ByAdapter, "kiro")
		if unmeasuredGroup.Tokens != nil {
			t.Errorf("by_adapter[kiro].Tokens = %+v, want nil (no measured run)", unmeasuredGroup.Tokens)
		}
		if unmeasuredGroup.CostUSD != nil {
			t.Errorf("by_adapter[kiro].CostUSD = %v, want nil", *unmeasuredGroup.CostUSD)
		}
		if unmeasuredGroup.TokensUnmeasuredRuns != 1 {
			t.Errorf("by_adapter[kiro].TokensUnmeasuredRuns = %d, want 1", unmeasuredGroup.TokensUnmeasuredRuns)
		}
	})

	t.Run("text form labels the measured-runs lines and dashes the unmeasured adapter row", func(t *testing.T) {
		t.Parallel()

		rates := server.TokenRates{
			"claude-code": server.TokenRateConfig{InputPerMtok: new(float64(10)), OutputPerMtok: new(float64(0))},
			"kiro":        server.TokenRateConfig{InputPerMtok: new(float64(10)), OutputPerMtok: new(float64(0))},
		}
		agg := runstats.New(fullCaps, statsPricer(rates))
		addRows(t, agg, measuredUnmeasuredRows()...)
		report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

		var stdout, stderr bytes.Buffer
		renderStatsText(&stdout, &stderr, report)
		out := stdout.String()

		if !strings.Contains(out, "tokens (measured runs)") {
			t.Errorf("stdout = %q, want the %q label", out, "tokens (measured runs)")
		}
		if !strings.Contains(out, "cost (measured runs)") {
			t.Errorf("stdout = %q, want the %q label", out, "cost (measured runs)")
		}
		if strings.Contains(out, "tokens (all runs)") || strings.Contains(out, "cost (all runs)") {
			t.Errorf("stdout = %q, want no trace of the old \"(all runs)\" labels", out)
		}

		unmeasuredLine := ""
		for line := range strings.SplitSeq(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "kiro") {
				unmeasuredLine = line
				break
			}
		}
		if unmeasuredLine == "" {
			t.Fatalf("stdout = %q, want a kiro row in the by-coding-agent table", out)
		}
		fields := strings.Fields(unmeasuredLine)
		if len(fields) == 0 || fields[len(fields)-1] != "-" || fields[len(fields)-2] != "-" {
			t.Errorf("kiro row = %q, want its total-tokens and cost columns to both be %q", unmeasuredLine, "-")
		}

		if !strings.Contains(out, "the token and cost figures skip 1 of these runs") {
			t.Errorf("stdout = %q, want the unmeasured-runs footnote", out)
		}
	})

	t.Run("footnote order places the unmeasured note after duration notes and before the unpriced-cost note", func(t *testing.T) {
		t.Parallel()

		summary := runstats.Summary{
			ZeroDurationRuns:     1,
			DurationExcludedRuns: 1,
			TokensUnmeasuredRuns: 1,
			CostUnpricedRuns:     1,
		}

		notes := statsFootnotes(summary, 0)
		if len(notes) != 4 {
			t.Fatalf("statsFootnotes(%+v) = %v, want 4 lines", summary, notes)
		}
		unmeasuredIdx := slices.IndexFunc(notes, func(n string) bool {
			return strings.Contains(n, "the token and cost figures skip")
		})
		unpricedIdx := slices.IndexFunc(notes, func(n string) bool {
			return strings.Contains(n, "the cost figures skip")
		})
		if unmeasuredIdx == -1 || unpricedIdx == -1 {
			t.Fatalf("statsFootnotes = %v, want both the unmeasured and unpriced notes present", notes)
		}
		if unmeasuredIdx != 2 {
			t.Errorf("unmeasured note at index %d, want 2 (after the two duration notes)", unmeasuredIdx)
		}
		if unmeasuredIdx >= unpricedIdx {
			t.Errorf("unmeasured note at index %d, unpriced note at index %d, want unmeasured before unpriced", unmeasuredIdx, unpricedIdx)
		}
	})

	t.Run("base tier reports zero unmeasured runs", func(t *testing.T) {
		t.Parallel()

		baseCaps := persistence.RunHistoryCapabilities{}
		agg := runstats.New(baseCaps, nil)
		addRows(t, agg, persistence.RunStatsRow{
			Status: "succeeded", AgentAdapter: "kiro",
			StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z",
		})

		report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

		if report.Summary.TokensUnmeasuredRuns != 0 {
			t.Errorf("Summary.TokensUnmeasuredRuns = %d, want 0 on the base tier (absent record, not zero unmeasured runs)", report.Summary.TokensUnmeasuredRuns)
		}
	})
}

func TestRunStatsEmptyRange(t *testing.T) {
	t.Parallel()

	newWorkspace := func(t *testing.T) string {
		t.Helper()
		return newStatsWorkspace(t, func(t *testing.T, dbPath string) {
			createStatsDB(t, dbPath, persistence.RunHistory{
				IssueID: "ISS-1", Identifier: "PROJ-1", Attempt: 1, AgentAdapter: "mock", Workspace: "/tmp/ws",
				StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z", Status: "succeeded",
			})
		})
	}

	t.Run("text", func(t *testing.T) {
		t.Parallel()

		wfPath := newWorkspace(t)
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"stats", "--since", "2099-01-01T00:00:00Z", wfPath}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run(stats) = %d, want 0; stderr: %s", code, stderr.String())
		}
		for _, want := range []string{
			"covering:  2099-01-01T00:00:00Z onward",
			"No runs finished in this range.",
		} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want to contain %q", stdout.String(), want)
			}
		}
		for _, absent := range []string{"by outcome", "by coding agent", "note:"} {
			if strings.Contains(stdout.String(), absent) {
				t.Errorf("stdout contains %q, want no breakdown or footnote for an empty range", absent)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		t.Parallel()

		wfPath := newWorkspace(t)
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"stats", "--format", "json", "--since", "2099-01-01T00:00:00Z", wfPath}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run(stats --format json) = %d, want 0; stderr: %s", code, stderr.String())
		}

		var report runstats.Report
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("json.Unmarshal(%q): %v", stdout.String(), err)
		}
		if report.Summary.Runs != 0 {
			t.Errorf("Summary.Runs = %d, want 0", report.Summary.Runs)
		}
		if report.Summary.Duration.Samples != 0 || report.Summary.Duration.P50 != nil {
			t.Errorf("Summary.Duration = %+v, want zero samples and null figures", report.Summary.Duration)
		}
		if report.Summary.MeanTurnsSucceeded != nil {
			t.Errorf("Summary.MeanTurnsSucceeded = %v, want nil", *report.Summary.MeanTurnsSucceeded)
		}
		if report.Summary.CostUSD != nil {
			t.Errorf("Summary.CostUSD = %v, want nil", *report.Summary.CostUSD)
		}
		for name, got := range map[string][]runstats.Group{
			"ByStatus": report.ByStatus, "ByAdapter": report.ByAdapter,
			"ByRule": report.ByRule, "ByTemplate": report.ByTemplate,
		} {
			if got == nil || len(got) != 0 {
				t.Errorf("%s = %v, want a non-nil empty slice", name, got)
			}
		}
		if report.ByChain == nil {
			t.Error("ByChain = nil, want a non-nil empty slice")
		}
		if report.SelfReview == nil {
			t.Fatal("SelfReview = nil, want non-nil on the full tier even with zero rows")
		}
		if report.SelfReview.RunsWithMetadata != 0 {
			t.Errorf("SelfReview.RunsWithMetadata = %d, want 0", report.SelfReview.RunsWithMetadata)
		}
		if report.SelfReview.ByFinalVerdict == nil {
			t.Error("SelfReview.ByFinalVerdict = nil, want a non-nil empty slice")
		}
	})
}

func TestRunStatsUsageErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	wfPath := writeCustomWorkflowFile(t, dir, statsWorkflow(""))

	tests := []struct {
		name string
		args []string
	}{
		{name: "invalid format", args: []string{"--format", "xml", wfPath}},
		{name: "since zero duration", args: []string{"--since", "0h", wfPath}},
		{name: "since negative duration", args: []string{"--since", "-1h", wfPath}},
		{name: "since bare number without unit", args: []string{"--since", "24", wfPath}},
		{name: "since timestamp without offset", args: []string{"--since", "2026-07-01T00:00:00", wfPath}},
		{name: "since not before until", args: []string{"--since", "2026-07-02T00:00:00Z", "--until", "2026-07-01T00:00:00Z", wfPath}},
		{name: "too many positional arguments", args: []string{wfPath, wfPath}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			args := append([]string{"stats"}, tt.args...)
			code := run(context.Background(), args, &stdout, &stderr)

			if code != 1 {
				t.Fatalf("run(stats %v) = %d, want 1; stderr: %s", tt.args, code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("run(stats %v) stdout = %q, want empty", tt.args, stdout.String())
			}
			lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
			if len(lines) != 1 || lines[0] == "" {
				t.Errorf("run(stats %v) stderr = %q, want exactly one line", tt.args, stderr.String())
			}
		})
	}
}

func TestRunStatsAgainstLiveWriter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	wfPath := writeCustomWorkflowFile(t, dir, statsWorkflow(""))
	dbPath := filepath.Join(dir, ".sortie.db")

	ctx := context.Background()
	store, err := persistence.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("persistence.Open(%q): %v", dbPath, err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := store.AppendRunHistory(ctx, persistence.RunHistory{
		IssueID: "ISS-1", Identifier: "PROJ-1", Attempt: 1, AgentAdapter: "mock", Workspace: "/tmp/ws",
		StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z", Status: "succeeded",
	}); err != nil {
		t.Fatalf("AppendRunHistory: %v", err)
	}

	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("os.Stat(%q): %v", dbPath, err)
	}

	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"stats", "--format", "json", wfPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(stats) against a live writable store = %d, want 0; stderr: %s", code, stderr.String())
	}

	var report runstats.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", stdout.String(), err)
	}
	if report.Summary.Runs != 1 {
		t.Errorf("Summary.Runs = %d, want 1", report.Summary.Runs)
	}

	after, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("os.Stat(%q): %v", dbPath, err)
	}
	if before.Size() != after.Size() {
		t.Errorf("database file size changed across the invocation: before %d, after %d", before.Size(), after.Size())
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("database file mtime changed across the invocation: before %v, after %v", before.ModTime(), after.ModTime())
	}
}

func TestRunStatsDegradedSchema(t *testing.T) {
	t.Parallel()

	t.Run("text", func(t *testing.T) {
		t.Parallel()

		wfPath := newStatsWorkspace(t, createLegacyStatsDB)
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"stats", wfPath}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run(stats) = %d, want 0; stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "was written before sortie recorded") {
			t.Errorf("stderr = %q, want a degraded-schema warning", stderr.String())
		}
		if !strings.HasPrefix(stderr.String(), "\nwarning: ") {
			t.Errorf("stderr = %q, want a blank line before the first warning so it does not run into the report",
				stderr.String())
		}
		if !strings.HasSuffix(stdout.String(), "\n") || strings.HasSuffix(stdout.String(), "\n\n") {
			t.Errorf("stdout = %q, want exactly one trailing newline: the separator belongs to stderr",
				stdout.String())
		}
		out := stdout.String()
		for _, absent := range []string{"by dispatch rule", "by prompt template", "self review", "turns (succeeded)", "tokens (measured runs)"} {
			if strings.Contains(out, absent) {
				t.Errorf("stdout contains %q, want it absent on the base tier", absent)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		t.Parallel()

		wfPath := newStatsWorkspace(t, createLegacyStatsDB)
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"stats", "--format", "json", wfPath}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run(stats --format json) = %d, want 0; stderr: %s", code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr = %q, want empty in JSON mode (warnings travel in the envelope)", stderr.String())
		}

		var report runstats.Report
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("json.Unmarshal(%q): %v", stdout.String(), err)
		}
		if report.SchemaTier != "base" {
			t.Errorf("SchemaTier = %q, want %q", report.SchemaTier, "base")
		}
		found := slices.ContainsFunc(report.Warnings, func(w string) bool {
			return strings.Contains(w, "was written before sortie recorded")
		})
		if !found {
			t.Errorf("Warnings = %v, want a degraded-schema warning", report.Warnings)
		}
		if len(report.ByRule) != 0 {
			t.Errorf("ByRule = %v, want empty on the base tier", report.ByRule)
		}
		if len(report.ByTemplate) != 0 {
			t.Errorf("ByTemplate = %v, want empty on the base tier", report.ByTemplate)
		}
		if report.SelfReview != nil {
			t.Errorf("SelfReview = %v, want nil on the base tier", report.SelfReview)
		}
		if report.Summary.Tokens != nil {
			t.Errorf("Summary.Tokens = %v, want nil on the base tier", report.Summary.Tokens)
		}
		if report.Summary.CostUSD != nil {
			t.Errorf("Summary.CostUSD = %v, want nil on the base tier", *report.Summary.CostUSD)
		}
		if report.Summary.TokensUnmeasuredRuns != 0 {
			t.Errorf("Summary.TokensUnmeasuredRuns = %d, want 0 on the base tier", report.Summary.TokensUnmeasuredRuns)
		}
	})
}

func TestRunStatsConfigErrors(t *testing.T) {
	t.Parallel()

	t.Run("missing workflow file", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"stats", "/nonexistent/sortie-test-workflow.md"}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("run(stats) = %d, want 1; stderr: %s", code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
		if !strings.Contains(stderr.String(), "workflow") {
			t.Errorf("stderr = %q, want to contain %q", stderr.String(), "workflow")
		}
	})

	t.Run("front matter value config.NewServiceConfig rejects", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		wfPath := writeCustomWorkflowFile(t, dir, statsWorkflow("db_path: 42\n"))

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"stats", wfPath}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("run(stats) = %d, want 1; stderr: %s", code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
		if !strings.Contains(stderr.String(), "db_path") {
			t.Errorf("stderr = %q, want to contain %q", stderr.String(), "db_path")
		}
	})

	t.Run("missing database file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		wfPath := writeCustomWorkflowFile(t, dir, statsWorkflow(""))

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"stats", wfPath}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("run(stats) = %d, want 1; stderr: %s", code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
		if !strings.Contains(stderr.String(), "sortie stats:") {
			t.Errorf("stderr = %q, want the %q diagnostic prefix", stderr.String(), "sortie stats:")
		}
	})
}

func TestRunStats_MissingCacheWriteTokensColumnMatchesMigratedCopy(t *testing.T) {
	t.Parallel()

	const inputTokens, outputTokens, cacheReadTokens = 1_000_000, 200_000, 100_000
	extra := "token_rates:\n  claude-code:\n    input_per_mtok: 5\n    output_per_mtok: 25\n    cache_read_per_mtok: 0.5\n"

	runReport := func(t *testing.T, wfPath string) runstats.Report {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"stats", "--format", "json", wfPath}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run(stats) = %d, want 0; stderr: %s", code, stderr.String())
		}
		var report runstats.Report
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("json.Unmarshal(%q): %v", stdout.String(), err)
		}
		return report
	}

	missingColumnDir := t.TempDir()
	missingColumnWfPath := writeCustomWorkflowFile(t, missingColumnDir, statsWorkflow(extra))
	createFullSchemaWithoutCacheWriteTokensDB(t, filepath.Join(missingColumnDir, ".sortie.db"), inputTokens, outputTokens, cacheReadTokens)
	missingColumn := runReport(t, missingColumnWfPath)

	migratedDir := t.TempDir()
	migratedWfPath := writeCustomWorkflowFile(t, migratedDir, statsWorkflow(extra))
	createStatsDB(t, filepath.Join(migratedDir, ".sortie.db"), persistence.RunHistory{
		IssueID: "ISS-CW", Identifier: "PROJ-CW", Attempt: 1, AgentAdapter: "claude-code", Workspace: "/tmp/ws",
		StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z", Status: "succeeded",
		TurnsCompleted: 1, InputTokens: inputTokens, OutputTokens: outputTokens,
		TotalTokens: inputTokens + outputTokens, CacheReadTokens: cacheReadTokens, TokensMeasured: true,
	})
	migratedCopy := runReport(t, migratedWfPath)

	for _, tt := range []struct {
		label  string
		report runstats.Report
	}{
		{"missing cache_write_tokens column", missingColumn},
		{"migrated copy", migratedCopy},
	} {
		if tt.report.SchemaTier != "full" {
			t.Errorf("%s: SchemaTier = %q, want %q", tt.label, tt.report.SchemaTier, "full")
		}
		if tt.report.Summary.Tokens == nil || tt.report.Summary.Tokens.CacheWrite != 0 {
			t.Errorf("%s: Summary.Tokens.CacheWrite = %v, want 0", tt.label, tt.report.Summary.Tokens)
		}
		if tt.report.Summary.CostUSD == nil {
			t.Fatalf("%s: Summary.CostUSD = nil, want a priced total", tt.label)
		}
	}
	if *missingColumn.Summary.CostUSD != *migratedCopy.Summary.CostUSD {
		t.Errorf("CostUSD = %v for the database missing cache_write_tokens, want %v (the migrated copy's cost)",
			*missingColumn.Summary.CostUSD, *migratedCopy.Summary.CostUSD)
	}
}

func TestRunStatsDBPathResolution(t *testing.T) {
	// No t.Parallel: the SORTIE_DB_PATH subtest uses t.Setenv, which
	// requires the calling test and every ancestor to be non-parallel.

	oneRun := func(t *testing.T) []persistence.RunHistory {
		t.Helper()
		return []persistence.RunHistory{{
			IssueID: "ISS-1", Identifier: "PROJ-1", Attempt: 1, AgentAdapter: "mock", Workspace: "/tmp/ws",
			StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z", Status: "succeeded",
		}}
	}

	t.Run("absolute db_path", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		dbDir := t.TempDir()
		dbPath := filepath.Join(dbDir, "custom.db")
		createStatsDB(t, dbPath, oneRun(t)...)
		wfPath := writeCustomWorkflowFile(t, dir, statsWorkflow(fmt.Sprintf("db_path: %q\n", dbPath)))

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"stats", "--format", "json", wfPath}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run(stats) = %d, want 0; stderr: %s", code, stderr.String())
		}
		var report runstats.Report
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("json.Unmarshal(%q): %v", stdout.String(), err)
		}
		if report.DBPath != dbPath {
			t.Errorf("DBPath = %q, want %q", report.DBPath, dbPath)
		}
		if report.Summary.Runs != 1 {
			t.Errorf("Summary.Runs = %d, want 1", report.Summary.Runs)
		}
	})

	t.Run("relative db_path resolves against the workflow directory", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		dbPath := filepath.Join(dir, "custom.db")
		createStatsDB(t, dbPath, oneRun(t)...)
		wfPath := writeCustomWorkflowFile(t, dir, statsWorkflow("db_path: custom.db\n"))

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"stats", "--format", "json", wfPath}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run(stats) = %d, want 0; stderr: %s", code, stderr.String())
		}
		var report runstats.Report
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("json.Unmarshal(%q): %v", stdout.String(), err)
		}
		if report.DBPath != dbPath {
			t.Errorf("DBPath = %q, want %q (resolved against the workflow directory)", report.DBPath, dbPath)
		}
	})

	t.Run("SORTIE_DB_PATH environment override", func(t *testing.T) {
		// No t.Parallel: t.Setenv requires a sequential test.

		dir := t.TempDir()
		dbDir := t.TempDir()
		dbPath := filepath.Join(dbDir, "env-override.db")
		createStatsDB(t, dbPath, oneRun(t)...)
		t.Setenv("SORTIE_DB_PATH", dbPath)
		wfPath := writeCustomWorkflowFile(t, dir, statsWorkflow(""))

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"stats", "--format", "json", wfPath}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run(stats) = %d, want 0; stderr: %s", code, stderr.String())
		}
		var report runstats.Report
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("json.Unmarshal(%q): %v", stdout.String(), err)
		}
		if report.DBPath != dbPath {
			t.Errorf("DBPath = %q, want %q (from SORTIE_DB_PATH)", report.DBPath, dbPath)
		}
	})
}

func withModelBreakdown(caps persistence.RunHistoryCapabilities) persistence.RunHistoryCapabilities {
	caps.HasConfiguredSettings = true
	return caps
}

func modelRow(adapter, model, status string, input int64) persistence.RunStatsRow {
	return persistence.RunStatsRow{
		Status: status, AgentAdapter: adapter, ConfiguredModel: model,
		StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z",
		InputTokens: input, TokensMeasured: true,
	}
}

func renderText(report runstats.Report) string {
	var stdout, stderr bytes.Buffer
	renderStatsText(&stdout, &stderr, report)
	return stdout.String()
}

func TestStatsByModel_WithoutTheBreakdown(t *testing.T) {
	t.Parallel()

	agg := runstats.New(fullCaps, nil)
	addRows(t, agg, modelRow("claude-code", "model-a", "succeeded", 1))

	report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	text := renderText(report)
	emptyLists := []string{`"by_model":[]`, `"by_chain":[]`, `"by_trigger":[]`}
	absentTables := []string{"by model", "by retry trigger"}
	for _, want := range emptyLists {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON = %s, want %s, never null", data, want)
		}
	}
	for _, table := range absentTables {
		if strings.Contains(text, table) {
			t.Errorf("text = %s, want no %q table", text, table)
		}
	}
}

func chainRow(chain, rule, previous, result, status string) persistence.RunStatsRow {
	row := modelRow("claude-code", "model-a", status, 1_000)
	row.Identifier, row.ChainID, row.RuleName, row.StagePrevious, row.StageResult = "PROJ-"+chain, chain, rule, previous, result
	row.TurnsCompleted = 2
	return row
}

func TestStatsByChain_TextListsTheBusiestChainsOnly(t *testing.T) {
	t.Parallel()

	caps := withModelBreakdown(fullCaps)
	caps.HasStageChains = true
	agg := runstats.New(caps, nil)
	for i := range statsChainTextRows + 1 {
		addRows(t, agg, chainRow(fmt.Sprintf("chain-%02d", i), "specify", "", "advanced", "succeeded"))
	}

	report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

	out := renderText(report)
	if len(report.ByChain) != statsChainTextRows+1 || strings.Count(out, "\n  chain-") != statsChainTextRows || strings.Contains(out, "chain-20") || !strings.Contains(out, "21") {
		t.Errorf("by_chain entries, text chain rows = %d, %d, want %d and %d, with the omitted chain-20 unprinted and the total 21 named in a note", len(report.ByChain), strings.Count(out, "\n  chain-"), statsChainTextRows+1, statsChainTextRows)
	}
}

func TestRunStatsByModelAgainstDatabases(t *testing.T) {
	t.Parallel()

	const warning = "this database was written before sortie recorded the model of each run, so the report leaves out the breakdown by model. Run sortie once with this workflow to add it."
	runOf := func(adapter, model, status string) persistence.RunHistory {
		return persistence.RunHistory{IssueID: "I-" + status, Identifier: "P-" + status, Attempt: 1, AgentAdapter: adapter, Workspace: "/tmp/ws", StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z", Status: status, ConfiguredModel: model}
	}
	tests := []struct {
		name        string
		build       func(t *testing.T, dbPath string)
		wantRuns    map[string]int
		wantWarning bool
	}{
		{"migrated database groups runs by model", func(t *testing.T, dbPath string) {
			createStatsDB(t, dbPath, runOf("mock", "model-a", "succeeded"), runOf("mock", "", "failed"))
		}, map[string]int{"model-a": 1, "<none>": 1}, false},
		{"full tier database without the column warns and leaves the list empty", func(t *testing.T, dbPath string) {
			createFullSchemaWithoutCacheWriteTokensDB(t, dbPath, 1, 1, 1)
		}, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"stats", "--format", "json", newStatsWorkspace(t, tt.build)}, &stdout, &stderr)
			var report runstats.Report
			if err := json.Unmarshal(stdout.Bytes(), &report); code != 0 || err != nil {
				t.Fatalf("run(stats) = %d, %v, want 0; stdout: %s; stderr: %s", code, err, stdout.String(), stderr.String())
			}

			for name, runs := range tt.wantRuns {
				if got := findGroup(t, report.ByModel, name).Runs; got != runs {
					t.Errorf("by_model group %q Runs = %d, want %d", name, got, runs)
				}
			}
			if len(report.ByModel) != len(tt.wantRuns) || report.ByModel == nil || slices.Contains(report.Warnings, warning) != tt.wantWarning {
				t.Errorf("ByModel, Warnings = %#v, %q, want %d groups (non-nil) and the configured-model warning present: %v", report.ByModel, report.Warnings, len(tt.wantRuns), tt.wantWarning)
			}
		})
	}
}
