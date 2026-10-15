package issuekit

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/sortie-ai/sortie/internal/domain"
)

// TransitionPlan is the ordered writes that move one issue to a target
// state.
type TransitionPlan struct {
	// ReopenFirst reopens the issue before any label write.
	ReopenFirst bool

	// Add is the target label; empty when the issue already carries it.
	Add string

	// Remove lists the configured states whose labels go, in removal order.
	Remove []string

	// Close closes the issue after the label writes.
	Close bool

	// ReopenLast reopens the issue after the label writes.
	ReopenLast bool
}

// TransitionOps are the tracker calls [ApplyTransition] drives for one
// issue.
type TransitionOps struct {
	// Labels is driven through [AddIssueLabel] and [RemoveIssueLabel].
	Labels IssueLabelOps

	// Swap sends the label changes as one atomic request. When set, Labels
	// is unused.
	Swap func(ctx context.Context, add string, remove []string) error

	// SetClosed closes or reopens the issue.
	SetClosed func(ctx context.Context, closed bool) error
}

// PlanTransition returns the writes that move an issue carrying labels, open
// or closed, to target.
//
// The target label goes on before any other state label comes off, and the
// label that decides the issue's current state comes off last, so every
// reading between two writes is the starting state or the target. A close
// follows the label writes and a reopen of an issue holding a terminal label
// precedes them, so a closed issue reads terminal until the labels no longer
// say so.
//
// A target outside the active, terminal, and handoff states, and a handoff
// target on a closed issue, return [domain.ErrTrackerPayload].
func (s LabelStates) PlanTransition(target string, labels []string, closed bool) (TransitionPlan, error) {
	t := strings.ToLower(target)
	inActive := slices.Contains(s.Active, t)
	inTerminal := slices.Contains(s.Terminal, t)
	if !inActive && !inTerminal && (s.Handoff == "" || t != s.Handoff) {
		return TransitionPlan{}, &domain.TrackerError{
			Kind:    domain.ErrTrackerPayload,
			Message: fmt.Sprintf("invalid target state: %q is not a configured active, terminal, or handoff state", target),
		}
	}
	if closed && !inActive && !inTerminal {
		return TransitionPlan{}, &domain.TrackerError{
			Kind:    domain.ErrTrackerPayload,
			Message: "issue is closed",
		}
	}

	start, carried := s.Derive(labels, closed)
	stale := slices.DeleteFunc(slices.Clone(carried), func(name string) bool { return name == t })
	if i := slices.Index(stale, start); i >= 0 {
		stale = append(slices.Delete(stale, i, i+1), start)
	}

	plan := TransitionPlan{Remove: stale}
	if !slices.Contains(carried, t) {
		plan.Add = t
	}
	plan.ReopenFirst = closed && inActive && s.carriesTerminal(labels)
	plan.Close = !closed && inTerminal
	plan.ReopenLast = closed && inActive && !plan.ReopenFirst
	return plan, nil
}

func (s LabelStates) carriesTerminal(labels []string) bool {
	return slices.ContainsFunc(s.Terminal, func(name string) bool { return carriesState(labels, name) })
}

// ApplyTransition runs plan against ops in order and returns the first error
// without running later steps. An empty plan sends nothing.
func ApplyTransition(ctx context.Context, plan TransitionPlan, ops TransitionOps) error {
	if plan.ReopenFirst {
		if err := ops.SetClosed(ctx, false); err != nil {
			return err
		}
	}

	if ops.Swap != nil {
		if plan.Add != "" || len(plan.Remove) > 0 {
			if err := ops.Swap(ctx, plan.Add, plan.Remove); err != nil {
				return err
			}
		}
	} else {
		if plan.Add != "" {
			if err := AddIssueLabel(ctx, plan.Add, ops.Labels); err != nil {
				return err
			}
		}
		for _, state := range plan.Remove {
			if err := RemoveIssueLabel(ctx, state, ops.Labels); err != nil {
				return err
			}
		}
	}

	if plan.Close {
		if err := ops.SetClosed(ctx, true); err != nil {
			return err
		}
	}
	if plan.ReopenLast {
		return ops.SetClosed(ctx, false)
	}
	return nil
}
