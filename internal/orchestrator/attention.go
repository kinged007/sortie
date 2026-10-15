package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"
)

// Attention kinds: the values of [AttentionCause.Kind] and of an attention
// item's kind.
const (
	AttentionKindParked               = "parked"
	AttentionKindBudgetHeld           = "budget_held"
	AttentionKindReactionEscalated    = "reaction_escalated"
	AttentionKindWorkflowReloadFailed = "workflow_reload_failed"
	AttentionKindPreflightFailed      = "preflight_failed"
)

// AttentionCause is one thing that needs a person, with the identity its
// attention item carries. Exactly one source member is set: the one Kind
// names.
type AttentionCause struct {
	ID    string
	Kind  string
	Since *time.Time // UTC; nil only for a park whose stored time did not parse

	Parked    *SnapshotParkedEntry
	Budget    *SnapshotBudgetEntry
	Reaction  *SnapshotPendingReaction
	Workflow  *SnapshotWorkflowHealth
	Preflight *SnapshotHealthCheck
}

// AttentionCauses returns every cause in snap and health that needs a person,
// in no defined order. A nil health yields no process-wide cause.
func AttentionCauses(snap RuntimeSnapshotResult, health *SnapshotHealth) []AttentionCause {
	causes := make([]AttentionCause, 0, len(snap.Parked)+len(snap.BudgetExhausted)+len(snap.PendingReactions)+2)
	for _, row := range snap.Parked {
		causes = append(causes, parkedCause(row))
	}
	for _, row := range snap.BudgetExhausted {
		causes = append(causes, budgetCause(row))
	}
	for _, row := range snap.PendingReactions {
		if row.EscalatedAt != nil {
			causes = append(causes, escalationCause(row))
		}
	}
	if health == nil {
		return causes
	}
	if health.Workflow.ConsecutiveFailures > 0 {
		causes = append(causes, workflowReloadCause(health.Workflow))
	}
	if health.Preflight.ConsecutiveFailures > 0 && !health.Preflight.WorkflowLoadFailed {
		causes = append(causes, preflightCause(health.Preflight.SnapshotHealthCheck))
	}
	return causes
}

func parkedCause(row SnapshotParkedEntry) AttentionCause {
	// A restart restores the park at whole seconds, so both sides of it must
	// hash and report the same precision.
	var since *time.Time
	var parkedAt string
	if !row.ParkedAt.IsZero() {
		truncated := row.ParkedAt.UTC().Truncate(time.Second)
		since = &truncated
		parkedAt = truncated.Format(time.RFC3339)
	}
	return AttentionCause{
		ID:     attentionID(AttentionKindParked, row.IssueID, parkedAt),
		Kind:   AttentionKindParked,
		Since:  since,
		Parked: &row,
	}
}

func budgetCause(row SnapshotBudgetEntry) AttentionCause {
	ceiling := strconv.Itoa(row.BudgetSessions)
	if row.Reason == budgetReasonToken {
		ceiling = strconv.FormatInt(row.BudgetTokens, 10)
	}
	since := row.ExhaustedAt.UTC()
	return AttentionCause{
		ID:     attentionID(AttentionKindBudgetHeld, row.IssueID, row.Reason, ceiling),
		Kind:   AttentionKindBudgetHeld,
		Since:  &since,
		Budget: &row,
	}
}

func escalationCause(row SnapshotPendingReaction) AttentionCause {
	since := row.EscalatedAt.UTC()
	return AttentionCause{
		ID:       attentionID(AttentionKindReactionEscalated, row.IssueID, row.Kind, since.Format(time.RFC3339Nano)),
		Kind:     AttentionKindReactionEscalated,
		Since:    &since,
		Reaction: &row,
	}
}

func workflowReloadCause(workflow SnapshotWorkflowHealth) AttentionCause {
	since := workflow.FailingSince.UTC()
	return AttentionCause{
		ID:       attentionID(AttentionKindWorkflowReloadFailed, since.Format(time.RFC3339Nano)),
		Kind:     AttentionKindWorkflowReloadFailed,
		Since:    &since,
		Workflow: &workflow,
	}
}

func preflightCause(check SnapshotHealthCheck) AttentionCause {
	since := check.FailingSince.UTC()
	return AttentionCause{
		ID:        attentionID(AttentionKindPreflightFailed, since.Format(time.RFC3339Nano)),
		Kind:      AttentionKindPreflightFailed,
		Since:     &since,
		Preflight: &check,
	}
}

// attentionID hashes the kind and its identity parts into the item id. The
// encoding is part of the contract: it never changes.
func attentionID(kind string, parts ...string) string {
	encoded, _ := json.Marshal(append([]string{kind}, parts...))
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:16])
}
