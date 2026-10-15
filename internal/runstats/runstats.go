// Package runstats provides the run statistics envelope and the aggregation
// that computes it from persisted run history.
//
// [New] constructs an [Aggregator] for the capabilities of the database being
// read. [Aggregator.Add] folds one [persistence.RunStatsRow] at a time, and
// [Aggregator.Report] returns the finished [Report]. The package imports only
// the domain and persistence layers, so any caller above them can serve the
// same figures.
package runstats

import "github.com/sortie-ai/sortie/internal/domain"

// Pricer returns the estimated USD cost of usage under the token rate the
// workflow configures for agentKind, or nil when no configured rate prices it.
type Pricer func(agentKind string, usage domain.TokenUsage) *float64

// Report is the "sortie stats --format json" envelope. Every slice
// field, by_trigger and by_origin included, is never nil; every pointer field is null
// when the figure it carries is unavailable.
type Report struct {
	GeneratedAt  string       `json:"generated_at"`
	WorkflowPath string       `json:"workflow_path"`
	DBPath       string       `json:"db_path"`
	Since        *string      `json:"since"`
	Until        *string      `json:"until"`
	SchemaTier   string       `json:"schema_tier"`
	Warnings     []string     `json:"warnings"`
	Summary      Summary      `json:"summary"`
	ByStatus     []Group      `json:"by_status"`
	ByAdapter    []Group      `json:"by_adapter"`
	ByRule       []Group      `json:"by_rule"`
	ByTemplate   []Group      `json:"by_template"`
	ByModel      []Group      `json:"by_model"`
	ByChain      []ChainGroup `json:"by_chain"`
	ByTrigger    []Group      `json:"by_trigger"`
	ByOrigin     []Group      `json:"by_origin"`
	SelfReview   *SelfReview  `json:"self_review"`

	// RatesConfigured lets the text renderer tell "no token_rates" from
	// "no run was priced"; it stays out of the JSON report.
	RatesConfigured bool `json:"-"`
}

// Summary reports the report-wide figures. MeanTurnsSucceeded,
// Tokens, CostUSD, and CostPerSucceededRunUSD are null on the base
// schema tier; CostUSD and CostPerSucceededRunUSD are also null when
// no priced run exists in range. Succeeded, Failed, Stopped, and Cancelled
// add up to Runs.
type Summary struct {
	Runs                   int      `json:"runs"`
	Issues                 int      `json:"issues"`
	Succeeded              int      `json:"succeeded"`
	Failed                 int      `json:"failed"`
	Stopped                int      `json:"stopped"`
	Cancelled              int      `json:"cancelled"`
	SuccessRate            float64  `json:"success_rate"`
	Duration               Duration `json:"duration_seconds"`
	MeanTurnsSucceeded     *float64 `json:"mean_turns_succeeded"`
	Tokens                 *Tokens  `json:"tokens"`
	CostUSD                *float64 `json:"cost_usd"`
	CostPerSucceededRunUSD *float64 `json:"cost_per_succeeded_run_usd"`
	ZeroDurationRuns       int      `json:"zero_duration_runs"`
	DurationExcludedRuns   int      `json:"duration_excluded_runs"`
	CostUnpricedRuns       int      `json:"cost_unpriced_runs"`
	TokensUnmeasuredRuns   int      `json:"tokens_unmeasured_runs"`
}

// Duration reports p50, p95, and the arithmetic mean over a sample
// population, in seconds. All three are null when Samples is 0.
type Duration struct {
	P50     *float64 `json:"p50"`
	P95     *float64 `json:"p95"`
	Mean    *float64 `json:"mean"`
	Samples int      `json:"samples"`
}

// Tokens reports the five token sums as stored, never recomputed.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	Total      int64 `json:"total"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// Group is one row of a by_status, by_adapter, by_rule, by_template,
// by_model, by_trigger, or by_origin breakdown. MeanTurns, Tokens, CostUSD, and
// CostPerSucceededRunUSD are null on the base schema tier; CostUSD and
// CostPerSucceededRunUSD are also null when the group holds no priced
// run. In by_status, Succeeded and SuccessRate are structural rather
// than informative: the "succeeded" row necessarily reports Succeeded
// equal to Runs. CostUnpricedRuns counts the group's measured runs, other
// than ci_failed, whose agent kind no configured rate prices; it is 0 on
// the base tier and while no rates are configured, and CostUSD leaves
// those runs out.
type Group struct {
	Name                   string   `json:"name"`
	Runs                   int      `json:"runs"`
	Succeeded              int      `json:"succeeded"`
	SuccessRate            float64  `json:"success_rate"`
	Share                  float64  `json:"share"`
	Duration               Duration `json:"duration_seconds"`
	MeanTurns              *float64 `json:"mean_turns"`
	Tokens                 *Tokens  `json:"tokens"`
	CostUSD                *float64 `json:"cost_usd"`
	CostPerSucceededRunUSD *float64 `json:"cost_per_succeeded_run_usd"`
	TokensUnmeasuredRuns   int      `json:"tokens_unmeasured_runs"`
	CostUnpricedRuns       int      `json:"cost_unpriced_runs"`
}

// ChainGroup is one by_chain entry; Name is the chain identifier.
// by_chain lists only chains where a hop was due or made, so it is a
// listing rather than a partition of the report's runs.
type ChainGroup struct {
	Group
	Issue   string   `json:"issue"`
	Stages  []string `json:"stages"`
	LastHop string   `json:"last_hop"`
}

// SelfReview reports the self-review aggregation. MeanIterations is
// null when RunsWithMetadata is 0. Present only on the full schema tier.
type SelfReview struct {
	RunsWithMetadata int            `json:"runs_with_metadata"`
	ByFinalVerdict   []VerdictCount `json:"by_final_verdict"`
	CapReachedRuns   int            `json:"cap_reached_runs"`
	MeanIterations   *float64       `json:"mean_iterations"`
	UnparsedRuns     int            `json:"unparsed_runs"`
}

// VerdictCount is one entry of [SelfReview.ByFinalVerdict].
type VerdictCount struct {
	Verdict string `json:"verdict"`
	Runs    int    `json:"runs"`
}
