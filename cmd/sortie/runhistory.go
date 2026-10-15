package main

import (
	"context"
	"fmt"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/orchestrator"
	"github.com/sortie-ai/sortie/internal/persistence"
	"github.com/sortie-ai/sortie/internal/server"
)

// runHistoryReader serves the HTTP server's run history from the store, so
// the server never names a persistence type.
type runHistoryReader struct {
	store *persistence.Store
}

var _ server.RunHistoryReader = runHistoryReader{}

// QueryRuns returns the runs matching q, newest first.
func (r runHistoryReader) QueryRuns(ctx context.Context, q server.RunHistoryQuery) ([]server.RunHistoryEntry, error) {
	runs, err := r.store.QueryRunHistory(ctx, persistence.RunHistoryFilter{
		IssueID:         q.IssueID,
		Identifier:      q.Identifier,
		RuleName:        q.RuleName,
		Status:          q.Status,
		StageTarget:     q.StageTarget,
		CompletedSince:  q.Since,
		CompletedBefore: q.Until,
		EndedSince:      q.EndedSince,
		EndedBefore:     q.EndedUntil,
		BeforeID:        q.BeforeID,
		Limit:           q.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]server.RunHistoryEntry, len(runs))
	for i, run := range runs {
		out[i] = runHistoryEntry(run)
	}
	return out, nil
}

// QueryIssueGroups returns the issues whose runs match q, newest first.
func (r runHistoryReader) QueryIssueGroups(ctx context.Context, q server.IssueGroupQuery) ([]server.IssueGroupKey, error) {
	groups, err := r.store.QueryIssueGroups(ctx, persistence.IssueGroupFilter{
		IssueKey:    q.IssueKey,
		RuleName:    q.RuleName,
		Status:      q.Status,
		StageTarget: q.StageTarget,
		BeforeID:    q.BeforeID,
		Limit:       q.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]server.IssueGroupKey, len(groups))
	for i, g := range groups {
		out[i] = server.IssueGroupKey{IssueID: g.IssueID, NewestRunID: g.NewestRunID}
	}
	return out, nil
}

// IssueTotals sums the issue's spend over all of its recorded runs. An issue
// with no run yields zero totals.
func (r runHistoryReader) IssueTotals(ctx context.Context, issueID string) (server.IssueRunTotals, error) {
	usage, err := r.store.TokenUsageByIssue(ctx, issueID)
	if err != nil {
		return server.IssueRunTotals{}, fmt.Errorf("issue run totals: %w", err)
	}
	return server.IssueRunTotals{
		Usage: *orchestrator.SnapshotIssueUsageOf(&usage),
		Tokens: domain.TokenUsage{
			InputTokens:      usage.InputTokens,
			OutputTokens:     usage.OutputTokens,
			CacheReadTokens:  usage.CacheReadTokens,
			CacheWriteTokens: usage.CacheWriteTokens,
		},
	}, nil
}

// IssueOutcomes returns the outcome of each listed issue that has one.
func (r runHistoryReader) IssueOutcomes(ctx context.Context, issueIDs []string) (map[string]server.IssueOutcome, error) {
	stored, err := r.store.IssueOutcomes(ctx, issueIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]server.IssueOutcome, len(stored))
	for id, o := range stored {
		out[id] = server.IssueOutcome{
			Kind:              o.Kind,
			ObservedAt:        o.ObservedAt,
			State:             o.State,
			MergeReactionKind: o.MergeReactionKind,
			PRNumber:          o.PRNumber,
			PRRepository:      o.PRRepository,
		}
	}
	return out, nil
}

func runHistoryEntry(r persistence.RunHistory) server.RunHistoryEntry {
	return server.RunHistoryEntry{
		Identifier:       r.Identifier,
		DisplayID:        r.DisplayID,
		Attempt:          r.Attempt,
		Status:           r.Status,
		WorkflowFile:     r.WorkflowFile,
		StartedAt:        r.StartedAt,
		CompletedAt:      r.CompletedAt,
		Error:            r.Error,
		TurnsCompleted:   r.TurnsCompleted,
		RuleName:         r.RuleName,
		ChainID:          r.ChainID,
		StagePrevious:    r.StagePrevious,
		StageTarget:      r.StageTarget,
		StageResult:      r.StageResult,
		ID:               r.ID,
		IssueID:          r.IssueID,
		IssueTitle:       r.IssueTitle,
		IssueURL:         r.IssueURL,
		AgentKind:        r.AgentAdapter,
		ConfiguredModel:  r.ConfiguredModel,
		ConfiguredEffort: r.ConfiguredEffort,
		ReportedModel:    r.ReportedModel,
		DispatchedAt:     r.DispatchedAt,
		Tokens: domain.TokenUsage{
			InputTokens:      r.InputTokens,
			OutputTokens:     r.OutputTokens,
			TotalTokens:      r.TotalTokens,
			CacheReadTokens:  r.CacheReadTokens,
			CacheWriteTokens: r.CacheWriteTokens,
		},
		TokensMeasured:       r.TokensMeasured,
		UnaccountedTurns:     r.UnaccountedTurns,
		StagePreviousOutcome: r.StagePreviousOutcome,
		ReviewMetadata:       r.ReviewMetadata,
		OriginRecorded:       r.OriginRecorded,
		Trigger:              r.Trigger,
		ReactionKind:         r.ReactionKind,
		SessionID:            r.SessionID,
		SSHHost:              r.SSHHost,
	}
}
