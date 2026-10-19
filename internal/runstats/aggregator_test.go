package runstats

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/persistence"
)

var fullCaps = persistence.RunHistoryCapabilities{
	HasTurnsCompleted:   true,
	HasReviewMetadata:   true,
	HasRuleRouting:      true,
	HasTokens:           true,
	HasTokenMeasurement: true,
	HasAgentBlocked:     true,
}

var fixedNow = time.Date(2026, 8, 7, 9, 15, 0, 0, time.UTC)

func inputOnlyPricer(usdPerMtok map[string]float64) Pricer {
	return func(agentKind string, usage domain.TokenUsage) *float64 {
		rate, ok := usdPerMtok[agentKind]
		if !ok {
			return nil
		}
		cost := float64(usage.InputTokens) / 1_000_000 * rate
		return &cost
	}
}

func rangeFloats(lo, hi int) []float64 {
	out := make([]float64, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, float64(i))
	}
	return out
}

func addRows(t *testing.T, agg *Aggregator, rows ...persistence.RunStatsRow) {
	t.Helper()
	for _, r := range rows {
		if err := agg.Add(r); err != nil {
			t.Fatalf("Aggregator.Add(%+v): %v", r, err)
		}
	}
}

func findGroup(t *testing.T, groups []Group, name string) Group {
	t.Helper()
	for _, g := range groups {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("group %q not found in %+v", name, groups)
	return Group{}
}

func reviewMetaPtr(t *testing.T, m domain.ReviewMetadata) *string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal(%+v): %v", m, err)
	}
	return new(string(b))
}

func withModelBreakdown(caps persistence.RunHistoryCapabilities) persistence.RunHistoryCapabilities {
	caps.HasConfiguredSettings = true
	return caps
}

func withTriggerBreakdown(caps persistence.RunHistoryCapabilities) persistence.RunHistoryCapabilities {
	caps = withModelBreakdown(caps)
	caps.HasStageChains = true
	caps.HasTrigger = true
	caps.HasFirstAfterHop = true
	return caps
}

func triggerRow(issueID string, trigger *string) persistence.RunStatsRow {
	row := modelRow("claude-code", "model-a", "succeeded", 0)
	row.IssueID, row.Trigger = issueID, trigger
	return row
}

func reportedModelRow(configured, reported string) persistence.RunStatsRow {
	row := modelRow("claude-code", configured, "succeeded", 0)
	row.ReportedModel = reported
	return row
}

func modelRow(adapter, model, status string, input int64) persistence.RunStatsRow {
	return persistence.RunStatsRow{
		Status: status, AgentAdapter: adapter, ConfiguredModel: model,
		StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z",
		InputTokens: input, TokensMeasured: true,
	}
}

func chainRow(chain, rule, previous, result, status string) persistence.RunStatsRow {
	row := modelRow("claude-code", "model-a", status, 1_000)
	row.Identifier, row.ChainID, row.RuleName, row.StagePrevious, row.StageResult = "PROJ-"+chain, chain, rule, previous, result
	row.TurnsCompleted = 2
	return row
}

func sumRuns(groups []Group) int {
	var runs int
	for _, g := range groups {
		runs += g.Runs
	}
	return runs
}

func TestStatsSummaryFormulas(t *testing.T) {
	t.Parallel()

	t.Run("percentile and mean over the worked sample sets", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name     string
			samples  []float64
			wantP50  float64
			wantP95  float64
			wantMean float64
		}{
			{name: "single sample", samples: []float64{7}, wantP50: 7, wantP95: 7, wantMean: 7},
			{name: "four samples", samples: []float64{10, 20, 30, 40}, wantP50: 20, wantP95: 40, wantMean: 25},
			{name: "twenty samples", samples: rangeFloats(1, 20), wantP50: 10, wantP95: 19, wantMean: 10.5},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				got := durationStats(tt.samples)

				if got.Samples != len(tt.samples) {
					t.Fatalf("durationStats(%v).Samples = %d, want %d", tt.samples, got.Samples, len(tt.samples))
				}
				if got.P50 == nil || *got.P50 != tt.wantP50 {
					t.Errorf("durationStats(%v).P50 = %v, want %v", tt.samples, got.P50, tt.wantP50)
				}
				if got.P95 == nil || *got.P95 != tt.wantP95 {
					t.Errorf("durationStats(%v).P95 = %v, want %v", tt.samples, got.P95, tt.wantP95)
				}
				if got.Mean == nil || *got.Mean != tt.wantMean {
					t.Errorf("durationStats(%v).Mean = %v, want %v", tt.samples, got.Mean, tt.wantMean)
				}
			})
		}
	})

	t.Run("duration and turns normalize on succeeded rows; cost per succeeded run divides all spend", func(t *testing.T) {
		t.Parallel()

		agg := New(fullCaps, inputOnlyPricer(map[string]float64{"mock": 10}))

		addRows(t, agg,
			persistence.RunStatsRow{
				Status: "succeeded", AgentAdapter: "mock", TurnsCompleted: 4,
				StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:10:00Z",
				InputTokens: 1_000_000, TokensMeasured: true,
			},
			persistence.RunStatsRow{
				Status: "failed", AgentAdapter: "mock", TurnsCompleted: 8,
				StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T01:00:00Z",
				InputTokens: 500_000, TokensMeasured: true,
			},
		)

		report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

		if report.Summary.Duration.Samples != 1 {
			t.Fatalf("Summary.Duration.Samples = %d, want 1 (succeeded rows only)", report.Summary.Duration.Samples)
		}
		if got, want := *report.Summary.Duration.P50, 600.0; got != want {
			t.Errorf("Summary.Duration.P50 = %v, want %v", got, want)
		}
		if report.Summary.MeanTurnsSucceeded == nil || *report.Summary.MeanTurnsSucceeded != 4 {
			t.Errorf("Summary.MeanTurnsSucceeded = %v, want 4 (succeeded rows only)", report.Summary.MeanTurnsSucceeded)
		}
		if report.Summary.CostUSD == nil || *report.Summary.CostUSD != 15 {
			t.Fatalf("Summary.CostUSD = %v, want 15 (all rows: $10 + $5)", report.Summary.CostUSD)
		}
		if report.Summary.CostPerSucceededRunUSD == nil || *report.Summary.CostPerSucceededRunUSD != 15 {
			t.Errorf("Summary.CostPerSucceededRunUSD = %v, want 15 (cost_usd / succeeded, not succeeded-only spend / succeeded)",
				report.Summary.CostPerSucceededRunUSD)
		}
	})
}

func TestStatsGroupDisclosure(t *testing.T) {
	t.Parallel()

	agg := New(fullCaps, inputOnlyPricer(map[string]float64{"mock": 10}))

	addRows(t, agg, persistence.RunStatsRow{
		Status:         "ci_failed",
		StartedAt:      "2026-01-01T00:00:00Z",
		CompletedAt:    "2026-01-01T00:00:00Z",
		TokensMeasured: true,
	})
	for _, adapter := range []string{"mock", "unrated"} {
		addRows(t, agg, persistence.RunStatsRow{
			Status:         "succeeded",
			AgentAdapter:   adapter,
			RuleName:       "review",
			TemplateID:     "review",
			StartedAt:      "2026-01-01T00:00:00Z",
			CompletedAt:    "2026-01-01T00:01:00Z",
			InputTokens:    1_000_000,
			TotalTokens:    1_000_000,
			TokensMeasured: true,
		})
	}

	report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

	adapterGroup := findGroup(t, report.ByAdapter, "<none>")
	if adapterGroup.Duration.Samples != 1 {
		t.Errorf("<none> adapter group Duration.Samples = %d, want 1", adapterGroup.Duration.Samples)
	}
	if adapterGroup.Duration.P50 == nil || *adapterGroup.Duration.P50 != 0 {
		t.Errorf("<none> adapter group Duration.P50 = %v, want 0 (zero-duration sample)", adapterGroup.Duration.P50)
	}
	if adapterGroup.Tokens == nil || adapterGroup.Tokens.Total != 0 {
		t.Errorf("<none> adapter group Tokens = %v, want Total 0", adapterGroup.Tokens)
	}
	if adapterGroup.CostUSD != nil {
		t.Errorf("<none> adapter group CostUSD = %v, want nil (no rate entry for an empty agent_adapter)", *adapterGroup.CostUSD)
	}

	ruleGroup := findGroup(t, report.ByRule, "<none>")
	if ruleGroup.Runs != 1 {
		t.Errorf("<none> rule group Runs = %d, want 1", ruleGroup.Runs)
	}
	if ruleGroup.CostUnpricedRuns != 0 {
		t.Errorf("<none> rule group CostUnpricedRuns = %d, want 0 (a ci_failed row is never unpriced)", ruleGroup.CostUnpricedRuns)
	}
	pricedRuleGroup := findGroup(t, report.ByRule, "review")
	if pricedRuleGroup.CostUnpricedRuns != 1 {
		t.Errorf("review rule group CostUnpricedRuns = %d, want 1", pricedRuleGroup.CostUnpricedRuns)
	}
	if pricedRuleGroup.CostUSD == nil || *pricedRuleGroup.CostUSD != 10 {
		t.Errorf("review rule group CostUSD = %v, want 10 (the priced run only)", pricedRuleGroup.CostUSD)
	}
	templateGroup := findGroup(t, report.ByTemplate, "<none>")
	if templateGroup.Runs != 1 {
		t.Errorf("<none> template group Runs = %d, want 1", templateGroup.Runs)
	}

	if report.Summary.ZeroDurationRuns != 1 {
		t.Errorf("Summary.ZeroDurationRuns = %d, want 1", report.Summary.ZeroDurationRuns)
	}
	if report.Summary.CostUnpricedRuns != 1 {
		t.Errorf("Summary.CostUnpricedRuns = %d, want 1 (the unrated run; a ci_failed row records a CI verdict, not an agent session)", report.Summary.CostUnpricedRuns)
	}
}

func TestPricerReceivesEveryTokenCounter(t *testing.T) {
	t.Parallel()

	var got domain.TokenUsage
	agg := New(fullCaps, func(_ string, usage domain.TokenUsage) *float64 {
		got = usage
		return nil
	})
	addRows(t, agg, persistence.RunStatsRow{
		Status:           "succeeded",
		AgentAdapter:     "mock",
		StartedAt:        "2026-01-01T00:00:00Z",
		CompletedAt:      "2026-01-01T00:01:00Z",
		InputTokens:      100,
		OutputTokens:     20,
		TotalTokens:      120,
		CacheReadTokens:  30,
		CacheWriteTokens: 10,
		TokensMeasured:   true,
	})

	want := domain.TokenUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120, CacheReadTokens: 30, CacheWriteTokens: 10}
	if got != want {
		t.Errorf("Pricer usage = %+v, want %+v", got, want)
	}
}

func TestStatsSelfReview(t *testing.T) {
	t.Parallel()

	agg := New(fullCaps, nil)
	addRows(t, agg,
		persistence.RunStatsRow{
			Status: "succeeded", StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:01:00Z",
			ReviewMetadata: reviewMetaPtr(t, domain.ReviewMetadata{FinalVerdict: "pass", TotalIterations: 1}),
		},
		persistence.RunStatsRow{
			Status: "failed", StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:01:00Z",
			ReviewMetadata: reviewMetaPtr(t, domain.ReviewMetadata{FinalVerdict: "iterate", TotalIterations: 3, CapReached: true}),
		},
		persistence.RunStatsRow{
			Status: "failed", StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:01:00Z",
			ReviewMetadata: reviewMetaPtr(t, domain.ReviewMetadata{FinalVerdict: "", TotalIterations: 2}),
		},
		persistence.RunStatsRow{
			Status: "failed", StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:01:00Z",
			ReviewMetadata: new("{not-json"),
		},
	)

	report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

	sr := report.SelfReview
	if sr == nil {
		t.Fatal("SelfReview = nil, want non-nil on the full tier")
	}
	if sr.RunsWithMetadata != 3 {
		t.Errorf("RunsWithMetadata = %d, want 3", sr.RunsWithMetadata)
	}
	if sr.UnparsedRuns != 1 {
		t.Errorf("UnparsedRuns = %d, want 1", sr.UnparsedRuns)
	}
	if sr.CapReachedRuns != 1 {
		t.Errorf("CapReachedRuns = %d, want 1", sr.CapReachedRuns)
	}
	if sr.MeanIterations == nil || *sr.MeanIterations != 2 {
		t.Errorf("MeanIterations = %v, want 2 ((1+3+2)/3)", sr.MeanIterations)
	}

	wantVerdicts := map[string]int{"iterate": 1, "none": 1, "pass": 1}
	gotVerdicts := make(map[string]int, len(sr.ByFinalVerdict))
	var verdictOrder []string
	for _, vc := range sr.ByFinalVerdict {
		gotVerdicts[vc.Verdict] = vc.Runs
		verdictOrder = append(verdictOrder, vc.Verdict)
	}
	if !maps.Equal(gotVerdicts, wantVerdicts) {
		t.Errorf("ByFinalVerdict = %v, want %v", gotVerdicts, wantVerdicts)
	}
	if !slices.IsSorted(verdictOrder) {
		t.Errorf("ByFinalVerdict order = %v, want ascending by verdict", verdictOrder)
	}
}

func TestStatsStatusOrdering(t *testing.T) {
	t.Parallel()

	agg := New(fullCaps, nil)
	for _, status := range []string{"weird_status", "succeeded", "succeeded", "failed", "cancelled"} {
		addRows(t, agg, persistence.RunStatsRow{
			Status: status, StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:01:00Z",
		})
	}

	report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

	// Runs tie at 1 for the last three; ties break ascending by name.
	want := []string{"succeeded", "cancelled", "failed", "weird_status"}
	var got []string
	for _, g := range report.ByStatus {
		got = append(got, g.Name)
	}
	if !slices.Equal(got, want) {
		t.Errorf("ByStatus order = %v, want %v", got, want)
	}

	weird := findGroup(t, report.ByStatus, "weird_status")
	if weird.Runs != 1 {
		t.Errorf("weird_status group Runs = %d, want 1 (status set must not be hard-coded)", weird.Runs)
	}
}

func TestStatsOutcomeCounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status string
		want   [4]int
	}{
		{status: "succeeded", want: [4]int{1, 0, 0, 0}},
		{status: "failed", want: [4]int{0, 1, 0, 0}},
		{status: "ci_failed", want: [4]int{0, 1, 0, 0}},
		{status: "needs_person", want: [4]int{0, 0, 1, 0}},
		{status: "budget_stopped", want: [4]int{0, 0, 1, 0}},
		{status: "cancelled", want: [4]int{0, 0, 0, 1}},
		{status: "weird_status", want: [4]int{0, 1, 0, 0}},
		{status: "", want: [4]int{0, 1, 0, 0}},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()

			for _, caps := range []persistence.RunHistoryCapabilities{{}, fullCaps} {
				agg := New(caps, nil)
				addRows(t, agg, persistence.RunStatsRow{Status: tt.status})

				s := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil).Summary

				got := [4]int{s.Succeeded, s.Failed, s.Stopped, s.Cancelled}
				if got != tt.want || s.Runs != 1 {
					t.Errorf("Summary counts for status %q (caps %+v) = %v over %d runs, want %v over 1", tt.status, caps, got, s.Runs, tt.want)
				}
			}
		})
	}
}

func TestStatsAggregatorReport_UntilBound(t *testing.T) {
	t.Parallel()

	agg := New(fullCaps, nil)
	until := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	report := agg.Report(fixedNow, "/wf", "/db", nil, &until, nil)

	want := "2026-08-01T00:00:00Z"
	if report.Until == nil || *report.Until != want {
		t.Errorf("Aggregator.Report(until=%v).Until = %v, want %q", until, report.Until, want)
	}
}

func TestDegradedSchemaWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		caps   persistence.RunHistoryCapabilities
		want   []string
		absent []string
	}{
		{
			name: "nothing recorded names every group once",
			caps: persistence.RunHistoryCapabilities{},
			want: []string{
				"before sortie recorded turns, self-review results, dispatch-rule routing, tokens and cost",
				"stage chains, what started each run, the first run after each stage hop, which runs stopped to wait for a person",
				"Run sortie once with this workflow to add them.",
			},
			absent: []string{"does carry", "falls back"},
		},
		{
			name: "partly migrated discloses the figures it drops anyway",
			caps: persistence.RunHistoryCapabilities{HasTurnsCompleted: true, HasReviewMetadata: true},
			want: []string{
				"before sortie recorded dispatch-rule routing, tokens and cost",
				"also leaves out turns, self-review results, which this database does carry",
			},
		},
		{
			name: "only tokens absent still discloses the other three",
			caps: persistence.RunHistoryCapabilities{
				HasTurnsCompleted: true, HasReviewMetadata: true, HasRuleRouting: true,
			},
			want: []string{
				"before sortie recorded tokens and cost",
				"also leaves out turns, self-review results, dispatch-rule routing, which this database does carry",
			},
		},
		{
			name: "only the measurement record absent names it in plain language",
			caps: persistence.RunHistoryCapabilities{
				HasTurnsCompleted: true, HasReviewMetadata: true, HasRuleRouting: true, HasTokens: true,
			},
			want: []string{
				"before sortie recorded which runs the coding agent could measure",
				"also leaves out turns, self-review results, dispatch-rule routing, tokens and cost, which this database does carry",
			},
			absent: []string{"tokens_measured", "run_history"},
		},
		{
			name: "configured model group is listed as unrecorded when absent",
			caps: persistence.RunHistoryCapabilities{},
			want: []string{"before sortie recorded", "the model each run was configured with"},
		},
		{
			name:   "configured model group is listed among the carried ones when present",
			caps:   persistence.RunHistoryCapabilities{HasTurnsCompleted: true, HasConfiguredSettings: true},
			want:   []string{"the model each run was configured with, which this database does carry"},
			absent: []string{"before sortie recorded turns"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := degradedSchemaWarning(tt.caps)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("degradedSchemaWarning(%+v) = %q, want to contain %q", tt.caps, got, want)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(got, absent) {
					t.Errorf("degradedSchemaWarning(%+v) = %q, want it not to contain %q", tt.caps, got, absent)
				}
			}
		})
	}
}

func TestStatsByModel(t *testing.T) {
	t.Parallel()

	agg := New(withModelBreakdown(fullCaps), inputOnlyPricer(map[string]float64{"claude-code": 10}))
	addRows(t, agg,
		modelRow("claude-code", "model-a", "succeeded", 1_000_000),
		modelRow("claude-code", "model-a", "failed", 0),
		modelRow("codex", "model-b", "succeeded", 1_000_000),
		modelRow("claude-code", "", "succeeded", 1_000_000),
		reportedModelRow("", "model-a"),
		reportedModelRow("model-b", "model-a"),
	)

	report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

	if got := sumRuns(report.ByModel); got != report.Summary.Runs {
		t.Errorf("by_model run counts sum to %d, want the report's runs: %d", got, report.Summary.Runs)
	}
	modelA, modelB, none := findGroup(t, report.ByModel, "model-a"), findGroup(t, report.ByModel, "model-b"), findGroup(t, report.ByModel, "<none>")
	if modelA.Runs != 4 || modelA.Succeeded != 3 || modelB.Runs != 1 || none.Runs != 1 {
		t.Errorf("model-a runs, succeeded, model-b runs and <none> runs = %d, %d, %d, %d, want 4, 3, 1, 1 (a reported model wins over the configured one)",
			modelA.Runs, modelA.Succeeded, modelB.Runs, none.Runs)
	}
	if modelA.CostUSD == nil || *modelA.CostUSD != 10 || modelB.CostUSD != nil {
		t.Errorf("model-a, model-b CostUSD = %v, %v, want 10 (priced by its kind's rate) and nil (its kind has no rate)", modelA.CostUSD, modelB.CostUSD)
	}
}

func TestStatsByTrigger(t *testing.T) {
	t.Parallel()

	noTrigger := withTriggerBreakdown(fullCaps)
	noTrigger.HasTrigger = false
	tests := []struct {
		name string
		caps persistence.RunHistoryCapabilities
		want []string
	}{
		{"recorded triggers keep unknown apart from none", withTriggerBreakdown(fullCaps), []string{"ci_fix", "<none>", "<unknown>"}},
		{"column absent leaves the breakdown empty", noTrigger, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			agg := New(tt.caps, nil)
			addRows(t, agg, triggerRow("A", new("ci_fix")), triggerRow("A", nil), triggerRow("B", new("")), triggerRow("C", new("ci_fix")))

			report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

			var got []string
			for _, g := range report.ByTrigger {
				got = append(got, g.Name)
			}
			if !slices.Equal(got, tt.want) || report.Summary.Issues != 3 {
				t.Errorf("Report(%+v) by_trigger names, issues = %q, %d, want %q, 3", tt.caps, got, report.Summary.Issues, tt.want)
			}
			if len(got) > 0 && (sumRuns(report.ByTrigger) != report.Summary.Runs || report.ByTrigger[0].Runs != 2) {
				t.Errorf("Report(%+v) by_trigger = %+v, want runs summing to %d with ci_fix at 2", tt.caps, report.ByTrigger, report.Summary.Runs)
			}
			if len(got) == 0 && (report.ByOrigin == nil || len(report.ByOrigin) > 0) {
				t.Errorf("Report(%+v).ByOrigin = %#v, want an empty, non-nil list beside the empty by_trigger", tt.caps, report.ByOrigin)
			}
		})
	}
}

func originRow(trigger *string, reactionKind, stagePrevious string, firstAfterHop bool) persistence.RunStatsRow {
	row := modelRow("claude-code", "model-a", "succeeded", 0)
	row.Trigger, row.ReactionKind, row.StagePrevious, row.FirstAfterHop = trigger, reactionKind, stagePrevious, firstAfterHop
	return row
}

func TestStatsByOrigin(t *testing.T) {
	t.Parallel()

	noOriginColumn := withTriggerBreakdown(fullCaps)
	noOriginColumn.HasFirstAfterHop = false
	errorRetry := originRow(new("error"), "", "", false)
	errorRetry.Status = "failed"
	tests := []struct {
		name string
		caps persistence.RunHistoryCapabilities
		want map[string]int
	}{
		{
			name: "each origin forms its own group and only the first run after a hop is stage_hop",
			caps: withTriggerBreakdown(fullCaps),
			want: map[string]int{"poll_tick": 2, "stage_hop": 1, "error": 1, "ci_fix": 1, "review": 1, "<unknown>": 1},
		},
		{name: "first_after_hop column absent leaves the breakdown empty beside by_trigger", caps: noOriginColumn},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			agg := New(tt.caps, nil)
			addRows(t, agg,
				originRow(new(""), "", "", false),
				originRow(new(""), "", "specify", true),
				errorRetry,
				originRow(new("ci_fix"), "ci", "", false),
				originRow(new(""), "review", "", false),
				originRow(nil, "", "", false),
				originRow(new(""), "", "specify", false),
			)

			report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

			if report.ByOrigin == nil {
				t.Fatalf("Report(%+v).ByOrigin = nil, want a non-nil list", tt.caps)
			}
			got := map[string]int{}
			for _, g := range report.ByOrigin {
				got[g.Name] = g.Runs
			}
			if !maps.Equal(got, tt.want) {
				t.Errorf("Report(%+v) by_origin runs = %v, want %v", tt.caps, got, tt.want)
			}
			if len(tt.want) > 0 && sumRuns(report.ByOrigin) != report.Summary.Runs {
				t.Errorf("Report(%+v) by_origin = %+v, want runs summing to %d", tt.caps, report.ByOrigin, report.Summary.Runs)
			}
			if sumRuns(report.ByTrigger) != report.Summary.Runs {
				t.Errorf("Report(%+v) by_trigger = %+v, want runs summing to %d", tt.caps, report.ByTrigger, report.Summary.Runs)
			}
		})
	}
}

func TestStatsByChain(t *testing.T) {
	t.Parallel()

	caps := withModelBreakdown(fullCaps)
	caps.HasStageChains = true
	last := chainRow("chain-a", "", "plan", "ceiling", "failed")
	last.DisplayID = "owner/repo#1"
	agg := New(caps, nil)
	addRows(t, agg,
		chainRow("chain-a", "specify", "", "advanced", "succeeded"),
		chainRow("chain-b", "solo", "", "", "succeeded"),
		chainRow("chain-a", "plan", "specify", "", "succeeded"),
		chainRow("chain-c", "specify", "", "", "succeeded"),
		chainRow("chain-a", "plan", "specify", "", "failed"),
		chainRow("chain-b", "solo", "", "failed", "failed"),
		chainRow("", "plan", "specify", "advanced", "succeeded"),
		last,
	)

	report := agg.Report(fixedNow, "/wf", "/db", nil, nil, nil)

	if len(report.ByChain) != 2 {
		t.Fatalf("by_chain = %+v, want chain-a and chain-b only: a row without a chain and a chain without a hop stay out", report.ByChain)
	}
	a, b := report.ByChain[0], report.ByChain[1]
	if a.Name != "chain-a" || a.Runs != 4 || a.Issue != "owner/repo#1" || !slices.Equal(a.Stages, []string{"specify", "plan"}) || a.LastHop != "ceiling" {
		t.Errorf("by_chain[0] name, runs, issue, stages, last_hop = %q, %d, %q, %v, %q, want %q, 4, %q, [specify plan], %q", a.Name, a.Runs, a.Issue, a.Stages, a.LastHop, "chain-a", "owner/repo#1", "ceiling")
	}
	if b.Name != "chain-b" || b.Issue != "PROJ-chain-b" || b.LastHop != "failed" {
		t.Errorf("by_chain[1] name, issue, last_hop = %q, %q, %q, want %q, %q, %q", b.Name, b.Issue, b.LastHop, "chain-b", "PROJ-chain-b", "failed")
	}
	solo := findGroup(t, report.ByRule, "solo")
	solo.Name = "chain-b"
	if !reflect.DeepEqual(b.Group, solo) {
		t.Errorf("by_chain[1] figures = %+v, want the by_rule figures of the same rows: %+v", b.Group, solo)
	}
}

func TestAggregatorReport_WarningsComposition(t *testing.T) {
	t.Parallel()

	modelCaps := withModelBreakdown(fullCaps)
	chainCaps := modelCaps
	chainCaps.HasStageChains = true
	triggerCaps := withTriggerBreakdown(fullCaps)
	originlessCaps := triggerCaps
	originlessCaps.HasFirstAfterHop = false
	unblockedCaps := triggerCaps
	unblockedCaps.HasAgentBlocked = false
	callerWarnings := []string{"token_rates: first", "token_rates: second"}

	tests := []struct {
		name    string
		caps    persistence.RunHistoryCapabilities
		stages  Stages
		extra   []string
		want    []string
		funnels bool
	}{
		{name: "every breakdown supported and no caller warnings", caps: triggerCaps, want: []string{}},
		{name: "chains configured and every breakdown supported", caps: triggerCaps, stages: threeStageChain, want: []string{}, funnels: true},
		{name: "funnel breakdown missing while chains exist", caps: unblockedCaps, stages: threeStageChain, extra: callerWarnings, want: append([]string{funnelBreakdownWarning}, callerWarnings...)},
		{name: "funnel breakdown missing without chains", caps: unblockedCaps, extra: callerWarnings, want: callerWarnings},
		{name: "trigger breakdown missing", caps: chainCaps, extra: callerWarnings, want: append([]string{triggerBreakdownWarning}, callerWarnings...)},
		{name: "origin breakdown missing", caps: originlessCaps, extra: callerWarnings, want: append([]string{originBreakdownWarning}, callerWarnings...)},
		{name: "chain breakdown missing", caps: modelCaps, extra: callerWarnings, want: append([]string{chainBreakdownWarning}, callerWarnings...)},
		{name: "model breakdown missing", caps: fullCaps, extra: callerWarnings, want: append([]string{modelBreakdownWarning}, callerWarnings...)},
		{name: "schema not full", caps: persistence.RunHistoryCapabilities{}, extra: callerWarnings, want: append([]string{degradedSchemaWarning(persistence.RunHistoryCapabilities{})}, callerWarnings...)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			agg := New(tt.caps, nil)
			agg.SetStages(tt.stages)

			report := agg.Report(fixedNow, "/wf", "/db", nil, nil, tt.extra)

			got := report.Warnings
			if got == nil {
				t.Fatalf("Report(caps=%+v, warnings=%v).Warnings = nil, want a non-nil list", tt.caps, tt.extra)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Report(caps=%+v, warnings=%v).Warnings = %q, want %q", tt.caps, tt.extra, got, tt.want)
			}
			if report.ChainFunnels == nil || (len(report.ChainFunnels) > 0) != tt.funnels {
				t.Errorf("Report(caps=%+v, chains=%v).ChainFunnels = %+v, want a non-nil list, non-empty %t", tt.caps, tt.stages.Chains, report.ChainFunnels, tt.funnels)
			}
		})
	}
}
