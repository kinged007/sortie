package runstats

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/persistence"
)

// tier reports the report's schema_tier label for caps.
func tier(caps persistence.RunHistoryCapabilities) string {
	if caps.Full() {
		return "full"
	}
	return "base"
}

// label maps an empty dimension value to the sentinel a reader sees for
// a run that carries no adapter, rule, template, trigger, or (in principle)
// status.
func label(v string) string {
	if v == "" {
		return "<none>"
	}
	return v
}

// triggerLabel names a retry trigger group. A nil trigger was never
// recorded, which differs from the empty trigger of a run nothing retried.
func triggerLabel(t *string) string {
	if t == nil {
		return "<unknown>"
	}
	return label(*t)
}

// originLabel names the run origin group of row, first match wins. A
// non-empty trigger decides before the reaction kind, so an error or stall
// retry of a reaction run lands in the same group as in by_trigger. A
// stage_hop is decided by FirstAfterHop alone: a later dispatch into a hopped
// stage carries the same StagePrevious.
func originLabel(row persistence.RunStatsRow) string {
	switch {
	case row.Trigger == nil:
		return "<unknown>"
	case *row.Trigger != "":
		return *row.Trigger
	case row.ReactionKind != "":
		return row.ReactionKind
	case row.Status == "ci_failed":
		return "<none>"
	case row.FirstAfterHop:
		return "stage_hop"
	default:
		return "poll_tick"
	}
}

// outcome names the one report-wide count a run with the given status
// belongs to. A status no count names is a failure, as the orchestrator
// records an unknown exit kind as failed too.
type outcome int

const (
	outcomeSucceeded outcome = iota
	outcomeFailed
	outcomeStopped
	outcomeCancelled
)

// outcomeOf maps a run status to its outcome.
func outcomeOf(status string) outcome {
	switch status {
	case "succeeded":
		return outcomeSucceeded
	case "needs_person", "budget_stopped":
		return outcomeStopped
	case "cancelled":
		return outcomeCancelled
	default:
		return outcomeFailed
	}
}

// duration computes row's duration in seconds from its stored RFC3339
// timestamps. ok is false when either timestamp fails to parse or the
// resulting difference is negative.
func duration(row persistence.RunStatsRow) (seconds float64, ok bool) {
	started, err := time.Parse(time.RFC3339, row.StartedAt)
	if err != nil {
		return 0, false
	}
	completed, err := time.Parse(time.RFC3339, row.CompletedAt)
	if err != nil {
		return 0, false
	}
	diff := completed.Sub(started).Seconds()
	if diff < 0 {
		return 0, false
	}
	return diff, true
}

// percentile returns the nearest-rank percentile p over sorted, which
// MUST already be sorted ascending. No interpolation is performed.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	idx := int(math.Ceil(p/100*float64(n))) - 1
	idx = max(0, min(idx, n-1))
	return sorted[idx]
}

// priceOf returns the estimated USD cost for row under price, or nil
// when price has no rate for row's agent adapter.
func priceOf(row persistence.RunStatsRow, price Pricer) *float64 {
	usage := domain.TokenUsage{
		InputTokens:      row.InputTokens,
		OutputTokens:     row.OutputTokens,
		TotalTokens:      row.TotalTokens,
		CacheReadTokens:  row.CacheReadTokens,
		CacheWriteTokens: row.CacheWriteTokens,
	}
	return price(row.AgentAdapter, usage)
}

// selfReviewAccum accumulates the self-review folding state across
// every row that carries non-nil review metadata.
type selfReviewAccum struct {
	runsWithMetadata int
	byVerdict        map[string]int
	capReachedRuns   int
	iterationSum     int
	unparsedRuns     int
}

// foldSelfReview unmarshals raw into a [domain.ReviewMetadata] and folds
// the result into acc. A value that fails to unmarshal increments acc's
// unparsed count and contributes to nothing else.
func foldSelfReview(raw string, acc *selfReviewAccum) {
	var metadata domain.ReviewMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		acc.unparsedRuns++
		return
	}

	acc.runsWithMetadata++
	verdict := metadata.FinalVerdict
	if verdict == "" {
		verdict = "none"
	}
	acc.byVerdict[verdict]++
	if metadata.CapReached {
		acc.capReachedRuns++
	}
	acc.iterationSum += metadata.TotalIterations
}

// groupAccum accumulates the folding state for one [Group] row
// before the derived figures are computed in [Aggregator.Report].
type groupAccum struct {
	name              string
	runs              int
	succeeded         int
	turnSum           int
	tokens            Tokens
	costUSD           float64
	pricedRuns        int
	costUnpricedRuns  int
	measuredRuns      int
	unmeasuredRuns    int
	succeededMeasured int
	durationSamples   []float64
}

// chainAccum accumulates the by_chain figures a groupAccum does
// not hold. hopInvolved is set once any row of the chain has a previous
// stage or a hop result.
type chainAccum struct {
	issue       string
	stages      []string
	lastHop     string
	hopInvolved bool
}

// totalAccum accumulates the report-wide folding state.
type totalAccum struct {
	runs                 int
	succeeded            int
	failed               int
	stopped              int
	cancelled            int
	succeededTurns       int
	tokens               Tokens
	costUSD              float64
	pricedRuns           int
	costUnpricedRuns     int
	measuredRuns         int
	unmeasuredRuns       int
	succeededMeasured    int
	zeroDurationRuns     int
	durationExcludedRuns int
	succeededSamples     []float64
}

// bumpGroup returns groups' entry for name, creating it on first
// use, and increments its run count and, when isSucceeded, its
// succeeded count.
func bumpGroup(groups map[string]*groupAccum, name string, isSucceeded bool) *groupAccum {
	g, ok := groups[name]
	if !ok {
		g = &groupAccum{name: name}
		groups[name] = g
	}
	g.runs++
	if isSucceeded {
		g.succeeded++
	}
	return g
}

// round1, round2, and round4 round v to the named number of decimal
// places so the JSON report is byte-stable.
func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// durationStats computes p50, p95, and the mean over samples, in
// seconds. All three are null when samples is empty. samples is sorted
// in place by a copy so the caller's slice is unmodified.
func durationStats(samples []float64) Duration {
	d := Duration{Samples: len(samples)}
	if len(samples) == 0 {
		return d
	}

	sorted := slices.Clone(samples)
	slices.Sort(sorted)

	var sum float64
	for _, s := range sorted {
		sum += s
	}

	d.P50 = new(percentile(sorted, 50))
	d.P95 = new(percentile(sorted, 95))
	d.Mean = new(round1(sum / float64(len(sorted))))
	return d
}

// compareGroups orders groups by descending runs, then ascending name.
func compareGroups(x, y Group) int {
	if x.Runs != y.Runs {
		return y.Runs - x.Runs
	}
	return strings.Compare(x.Name, y.Name)
}

// degradedSchemaWarning names the figures the report leaves out and states
// the remedy. It MUST NOT be called when caps is full.
//
// A partly migrated database is the common case, and on it the two lists
// differ: the report falls back to the basic figures wholesale, so it drops
// groups this database does carry as well as the ones it never recorded.
// Naming only the unrecorded groups would let a reader conclude the
// remaining figures are present when they are not.
func degradedSchemaWarning(caps persistence.RunHistoryCapabilities) string {
	groups := []struct {
		recorded bool
		name     string
	}{
		{caps.HasTurnsCompleted, "turns"},
		{caps.HasReviewMetadata, "self-review results"},
		{caps.HasRuleRouting, "dispatch-rule routing"},
		{caps.HasTokens, "tokens and cost"},
		{caps.HasTokenMeasurement, "which runs the coding agent could measure"},
		{caps.HasConfiguredSettings, "the model each run was configured with"},
		{caps.HasStageChains, "stage chains"},
		{caps.HasTrigger, "what started each run"},
		{caps.HasFirstAfterHop, "the first run after each stage hop"},
	}

	var unrecorded, dropped []string
	for _, g := range groups {
		if g.recorded {
			dropped = append(dropped, g.name)
		} else {
			unrecorded = append(unrecorded, g.name)
		}
	}

	if len(dropped) == 0 {
		return fmt.Sprintf(
			"this database was written before sortie recorded %s, so the report leaves them out. "+
				"Run sortie once with this workflow to add them.",
			strings.Join(unrecorded, ", "))
	}
	return fmt.Sprintf(
		"this database was written before sortie recorded %s. The report falls back to run counts and "+
			"durations, so it also leaves out %s, which this database does carry. "+
			"Run sortie once with this workflow to get the full report.",
		strings.Join(unrecorded, ", "), strings.Join(dropped, ", "))
}

// modelBreakdownWarning explains the missing by_model breakdown.
const modelBreakdownWarning = "this database was written before sortie recorded the model each run was configured with, " +
	"so the report leaves out the breakdown by configured model. " +
	"Run sortie once with this workflow to add it."

// chainBreakdownWarning explains the missing by_chain breakdown.
const chainBreakdownWarning = "this database was written before sortie recorded stage chains, " +
	"so the report leaves out the breakdown by stage chain. " +
	"Run sortie once with this workflow to add it."

// triggerBreakdownWarning explains the missing by_trigger and by_origin
// breakdowns.
const triggerBreakdownWarning = "this database was written before sortie recorded what started each run, " +
	"so the report leaves out the breakdowns by retry trigger and by run origin. " +
	"Run sortie once with this workflow to add them."

// originBreakdownWarning explains the missing by_origin breakdown on a
// database that does record what started each run.
const originBreakdownWarning = "this database was written before sortie recorded the first run after each stage hop, " +
	"so the report leaves out the breakdown by run origin. " +
	"Run sortie once with this workflow to add it."

// Aggregator folds a stream of [persistence.RunStatsRow] values, via
// [Aggregator.Add], into the totals and per-dimension groups
// [Aggregator.Report] renders into a [Report]. It is not safe for concurrent
// use; a caller builds one per report.
type Aggregator struct {
	caps            persistence.RunHistoryCapabilities
	price           Pricer
	ratesConfigured bool
	tierLabel       string

	total totalAccum

	byStatus   map[string]*groupAccum
	byAdapter  map[string]*groupAccum
	byRule     map[string]*groupAccum
	byTemplate map[string]*groupAccum
	byModel    map[string]*groupAccum
	byChain    map[string]*groupAccum
	byTrigger  map[string]*groupAccum
	byOrigin   map[string]*groupAccum
	chains     map[string]*chainAccum
	issueIDs   map[string]struct{}

	selfReview selfReviewAccum
}

// New constructs an [Aggregator] for caps and price. A nil price means the
// workflow configures no token_rates entry: no run is priced, none counts as
// unpriced, and [Report.RatesConfigured] is false.
func New(caps persistence.RunHistoryCapabilities, price Pricer) *Aggregator {
	return &Aggregator{
		caps:            caps,
		price:           price,
		ratesConfigured: price != nil,
		tierLabel:       tier(caps),
		byStatus:        make(map[string]*groupAccum),
		byAdapter:       make(map[string]*groupAccum),
		byRule:          make(map[string]*groupAccum),
		byTemplate:      make(map[string]*groupAccum),
		byModel:         make(map[string]*groupAccum),
		byChain:         make(map[string]*groupAccum),
		byTrigger:       make(map[string]*groupAccum),
		byOrigin:        make(map[string]*groupAccum),
		chains:          make(map[string]*chainAccum),
		issueIDs:        make(map[string]struct{}),
		selfReview:      selfReviewAccum{byVerdict: make(map[string]int)},
	}
}

// Add folds one row into the aggregator. It is the visit callback
// [persistence.Store.ScanRunHistoryRange] calls once per row; it calls
// no Store method and always returns nil.
func (a *Aggregator) Add(row persistence.RunStatsRow) error {
	isSucceeded := row.Status == "succeeded"
	a.total.runs++
	a.issueIDs[row.IssueID] = struct{}{}
	switch outcomeOf(row.Status) {
	case outcomeSucceeded:
		a.total.succeeded++
	case outcomeStopped:
		a.total.stopped++
	case outcomeCancelled:
		a.total.cancelled++
	default:
		a.total.failed++
	}

	bumped := []*groupAccum{
		bumpGroup(a.byStatus, label(row.Status), isSucceeded),
		bumpGroup(a.byAdapter, label(row.AgentAdapter), isSucceeded),
	}

	full := a.caps.Full()
	if full {
		bumped = append(bumped,
			bumpGroup(a.byRule, label(row.RuleName), isSucceeded),
			bumpGroup(a.byTemplate, label(row.TemplateID), isSucceeded),
		)
		if a.caps.ModelBreakdown() {
			bumped = append(bumped, bumpGroup(a.byModel, label(row.ConfiguredModel), isSucceeded))
		}
		if a.caps.ChainBreakdown() && row.ChainID != "" {
			bumped = append(bumped, bumpGroup(a.byChain, row.ChainID, isSucceeded))
			a.foldChain(row)
		}
		if a.caps.TriggerBreakdown() {
			bumped = append(bumped, bumpGroup(a.byTrigger, triggerLabel(row.Trigger), isSucceeded))
		}
		if a.caps.OriginBreakdown() {
			bumped = append(bumped, bumpGroup(a.byOrigin, originLabel(row), isSucceeded))
		}

		for _, g := range bumped {
			g.turnSum += row.TurnsCompleted
		}
		if isSucceeded {
			a.total.succeededTurns += row.TurnsCompleted
		}

		if !row.TokensMeasured {
			a.total.unmeasuredRuns++
			for _, g := range bumped {
				g.unmeasuredRuns++
			}
		} else {
			for _, g := range bumped {
				g.tokens.Input += row.InputTokens
				g.tokens.Output += row.OutputTokens
				g.tokens.Total += row.TotalTokens
				g.tokens.CacheRead += row.CacheReadTokens
				g.tokens.CacheWrite += row.CacheWriteTokens
				g.measuredRuns++
				if isSucceeded {
					g.succeededMeasured++
				}
			}
			a.total.tokens.Input += row.InputTokens
			a.total.tokens.Output += row.OutputTokens
			a.total.tokens.Total += row.TotalTokens
			a.total.tokens.CacheRead += row.CacheReadTokens
			a.total.tokens.CacheWrite += row.CacheWriteTokens
			a.total.measuredRuns++
			if isSucceeded {
				a.total.succeededMeasured++
			}

			// A ci_failed row records a CI verdict and carries no agent
			// kind, so it has no rate to look up and is not an unpriced run.
			if a.ratesConfigured && row.Status != "ci_failed" {
				cost := priceOf(row, a.price)
				if cost == nil {
					a.total.costUnpricedRuns++
					for _, g := range bumped {
						g.costUnpricedRuns++
					}
				} else {
					a.total.costUSD += *cost
					a.total.pricedRuns++
					for _, g := range bumped {
						g.costUSD += *cost
						g.pricedRuns++
					}
				}
			}
		}

		if row.ReviewMetadata != nil {
			foldSelfReview(*row.ReviewMetadata, &a.selfReview)
		}
	}

	seconds, ok := duration(row)
	if !ok {
		a.total.durationExcludedRuns++
		return nil
	}
	if seconds == 0 {
		a.total.zeroDurationRuns++
	}
	for _, g := range bumped {
		g.durationSamples = append(g.durationSamples, seconds)
	}
	if isSucceeded {
		a.total.succeededSamples = append(a.total.succeededSamples, seconds)
	}

	return nil
}

// foldChain folds row into its chain's by_chain accumulator. Rows arrive
// in ascending id order, so the last non-empty value of a field is the
// latest one.
func (a *Aggregator) foldChain(row persistence.RunStatsRow) {
	c, ok := a.chains[row.ChainID]
	if !ok {
		c = &chainAccum{}
		a.chains[row.ChainID] = c
	}
	c.issue = cmp.Or(row.DisplayID, row.Identifier)
	if row.RuleName != "" && (len(c.stages) == 0 || c.stages[len(c.stages)-1] != row.RuleName) {
		c.stages = append(c.stages, row.RuleName)
	}
	if row.StageResult != "" {
		c.lastHop = row.StageResult
	}
	if row.StagePrevious != "" || row.StageResult != "" {
		c.hopInvolved = true
	}
}

// chainReport lists the chains where a hop was due or made, sorted like
// every other breakdown.
func (a *Aggregator) chainReport() []ChainGroup {
	out := []ChainGroup{}
	for id, c := range a.chains {
		if !c.hopInvolved {
			continue
		}
		out = append(out, ChainGroup{
			Group:   a.groupStat(a.byChain[id], true),
			Issue:   c.issue,
			Stages:  append([]string{}, c.stages...),
			LastHop: c.lastHop,
		})
	}
	slices.SortFunc(out, func(x, y ChainGroup) int {
		return compareGroups(x.Group, y.Group)
	})
	return out
}

// Report computes every derived figure exactly once from a's folded
// state and returns the complete [Report]. Its Warnings field opens with the
// warning for the capabilities a was built with, if any, and continues with
// warnings appended in order after it.
func (a *Aggregator) Report(
	generatedAt time.Time,
	workflowPath, dbPath string,
	since, until *time.Time,
	warnings []string,
) Report {
	full := a.caps.Full()

	rpt := Report{
		GeneratedAt:  generatedAt.Format(time.RFC3339),
		WorkflowPath: workflowPath,
		DBPath:       dbPath,
		SchemaTier:   a.tierLabel,
		Warnings:     a.composeWarnings(warnings),
		Summary:      a.summary(full),
		ByStatus:     a.groupSlice(a.byStatus, full),
		ByAdapter:    a.groupSlice(a.byAdapter, full),
		ByRule:       []Group{},
		ByTemplate:   []Group{},
		ByModel:      []Group{},
		ByChain:      []ChainGroup{},
		ByTrigger:    []Group{},
		ByOrigin:     []Group{},

		RatesConfigured: a.ratesConfigured,
	}
	if since != nil {
		rpt.Since = new(since.UTC().Format(time.RFC3339))
	}
	if until != nil {
		rpt.Until = new(until.UTC().Format(time.RFC3339))
	}
	if full {
		rpt.ByRule = a.groupSlice(a.byRule, full)
		rpt.ByTemplate = a.groupSlice(a.byTemplate, full)
		if a.caps.ModelBreakdown() {
			rpt.ByModel = a.groupSlice(a.byModel, full)
		}
		if a.caps.ChainBreakdown() {
			rpt.ByChain = a.chainReport()
		}
		if a.caps.TriggerBreakdown() {
			rpt.ByTrigger = a.groupSlice(a.byTrigger, full)
		}
		if a.caps.OriginBreakdown() {
			rpt.ByOrigin = a.groupSlice(a.byOrigin, full)
		}
		rpt.SelfReview = a.selfReviewReport()
	}

	return rpt
}

// composeWarnings returns the non-nil warning list: the one warning that
// names what a's capabilities leave out, if any, followed by extra.
func (a *Aggregator) composeWarnings(extra []string) []string {
	list := []string{}
	switch {
	case !a.caps.Full():
		list = append(list, degradedSchemaWarning(a.caps))
	case !a.caps.ModelBreakdown():
		list = append(list, modelBreakdownWarning)
	case !a.caps.ChainBreakdown():
		list = append(list, chainBreakdownWarning)
	case !a.caps.TriggerBreakdown():
		list = append(list, triggerBreakdownWarning)
	case !a.caps.OriginBreakdown():
		list = append(list, originBreakdownWarning)
	}
	return append(list, extra...)
}

// summary computes the report-wide [Summary].
func (a *Aggregator) summary(full bool) Summary {
	s := Summary{
		Runs:                 a.total.runs,
		Issues:               len(a.issueIDs),
		Succeeded:            a.total.succeeded,
		Failed:               a.total.failed,
		Stopped:              a.total.stopped,
		Cancelled:            a.total.cancelled,
		Duration:             durationStats(a.total.succeededSamples),
		ZeroDurationRuns:     a.total.zeroDurationRuns,
		DurationExcludedRuns: a.total.durationExcludedRuns,
		CostUnpricedRuns:     a.total.costUnpricedRuns,
	}
	if s.Runs > 0 {
		s.SuccessRate = round4(float64(s.Succeeded) / float64(s.Runs))
	}
	if full && s.Succeeded > 0 {
		s.MeanTurnsSucceeded = new(round2(float64(a.total.succeededTurns) / float64(s.Succeeded)))
	}
	if full {
		s.TokensUnmeasuredRuns = a.total.unmeasuredRuns
	}
	if full && a.total.measuredRuns > 0 {
		s.Tokens = new(a.total.tokens)
	}
	if full && a.total.pricedRuns > 0 {
		cost := round2(a.total.costUSD)
		s.CostUSD = &cost
		if a.total.succeededMeasured > 0 {
			s.CostPerSucceededRunUSD = new(round2(cost / float64(a.total.succeededMeasured)))
		}
	}
	return s
}

// groupSlice converts groups into a [Group] slice, sorted by
// descending runs with ties broken by ascending name under byte
// comparison.
func (a *Aggregator) groupSlice(groups map[string]*groupAccum, full bool) []Group {
	out := make([]Group, 0, len(groups))
	for _, g := range groups {
		out = append(out, a.groupStat(g, full))
	}
	slices.SortFunc(out, compareGroups)
	return out
}

// groupStat computes the derived [Group] figures for one accumulated
// group.
func (a *Aggregator) groupStat(g *groupAccum, full bool) Group {
	stat := Group{
		Name:      g.name,
		Runs:      g.runs,
		Succeeded: g.succeeded,
		Duration:  durationStats(g.durationSamples),
	}
	if g.runs > 0 {
		stat.SuccessRate = round4(float64(g.succeeded) / float64(g.runs))
	}
	if a.total.runs > 0 {
		stat.Share = round4(float64(g.runs) / float64(a.total.runs))
	}
	if full {
		stat.MeanTurns = new(round2(float64(g.turnSum) / float64(g.runs)))
		stat.TokensUnmeasuredRuns = g.unmeasuredRuns
		stat.CostUnpricedRuns = g.costUnpricedRuns
		if g.measuredRuns > 0 {
			stat.Tokens = new(g.tokens)
		}
		if g.pricedRuns > 0 {
			cost := round2(g.costUSD)
			stat.CostUSD = &cost
			if g.succeededMeasured > 0 {
				stat.CostPerSucceededRunUSD = new(round2(cost / float64(g.succeededMeasured)))
			}
		}
	}
	return stat
}

// selfReviewReport computes the derived [SelfReview] from a's folded
// self-review state. by_final_verdict is sorted by ascending verdict.
func (a *Aggregator) selfReviewReport() *SelfReview {
	sr := &SelfReview{
		RunsWithMetadata: a.selfReview.runsWithMetadata,
		ByFinalVerdict:   make([]VerdictCount, 0, len(a.selfReview.byVerdict)),
		CapReachedRuns:   a.selfReview.capReachedRuns,
		UnparsedRuns:     a.selfReview.unparsedRuns,
	}
	if a.selfReview.runsWithMetadata > 0 {
		sr.MeanIterations = new(round2(float64(a.selfReview.iterationSum) / float64(a.selfReview.runsWithMetadata)))
	}
	for verdict, count := range a.selfReview.byVerdict {
		sr.ByFinalVerdict = append(sr.ByFinalVerdict, VerdictCount{Verdict: verdict, Runs: count})
	}
	slices.SortFunc(sr.ByFinalVerdict, func(x, y VerdictCount) int {
		return strings.Compare(x.Verdict, y.Verdict)
	})
	return sr
}
