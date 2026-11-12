package scmcore

import (
	"fmt"
	"strings"

	"github.com/sortie-ai/sortie/internal/domain"
)

// IsBotAuthor reports whether a review author is an automated bot.
// platformBot is the platform's own bot marker, false on a platform that
// has none; allowlist entries match login case-insensitively. The two
// conditions form a union: either qualifies.
func IsBotAuthor(login string, platformBot bool, allowlist []string) bool {
	if platformBot {
		return true
	}
	for _, name := range allowlist {
		if strings.EqualFold(login, name) {
			return true
		}
	}
	return false
}

// SortableEventID formats a numeric journal-event id as a fixed-width,
// zero-padded decimal so lexical string comparison matches numeric order.
// The width covers the maximum int64.
func SortableEventID(id int64) string {
	return fmt.Sprintf("%019d", id)
}

// ReviewFacts holds what an adapter observed about a pull request's review
// state, before they are folded into one [domain.ReviewDecision].
type ReviewFacts struct {
	ChangesRequested  bool // a changes request stands
	ApprovalRuleUnmet bool // an approval rule is unmet
	Approved          bool // the forge reports approval or its approval rules met
	ReviewerRequested bool // a requested or assigned reviewer has not approved
}

// FoldReviewDecision returns the decision of the first true fact in the
// order changes requested, approval rule unmet, approved, reviewer
// requested, and [domain.ReviewDecisionNotRequired] when none holds.
func FoldReviewDecision(facts ReviewFacts) domain.ReviewDecision {
	switch {
	case facts.ChangesRequested:
		return domain.ReviewDecisionChangesRequested
	case facts.ApprovalRuleUnmet:
		return domain.ReviewDecisionReviewRequired
	case facts.Approved:
		return domain.ReviewDecisionApproved
	case facts.ReviewerRequested:
		return domain.ReviewDecisionReviewRequested
	default:
		return domain.ReviewDecisionNotRequired
	}
}
