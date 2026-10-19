package server

import (
	"cmp"
	"slices"
	"time"
)

const (
	statusRunning          = "running"
	statusRetrying         = "retrying"
	statusBudgetExhausted  = "budget_exhausted"
	statusParked           = "parked"
	statusPendingReactions = "pending_reactions"
	statusBetweenStages    = "between_stages"
)

// activeIssueResponse is the JSON wire format for one active issue on
// GET /api/v1/state.
type activeIssueResponse struct {
	IssueID           string                 `json:"issue_id"`
	IssueIdentifier   string                 `json:"issue_identifier"`
	DisplayIdentifier string                 `json:"display_identifier,omitempty"`
	IssueTitle        string                 `json:"issue_title,omitempty"`
	IssueURL          string                 `json:"issue_url,omitempty"`
	Status            string                 `json:"status"`
	RuleName          string                 `json:"rule_name,omitempty"`
	StagePosition     *stagePositionResponse `json:"stage_position"`
	AgentKind         string                 `json:"agent_kind,omitempty"`
	ConfiguredModel   string                 `json:"configured_model,omitempty"`
	ReactionKind      string                 `json:"reaction_kind,omitempty"`
	DispatchedAt      *time.Time             `json:"dispatched_at"`
	StatusText        string                 `json:"status_text"`
	StatusTextFull    string                 `json:"status_text_full"`

	// PRLoop is what each configured pull request reaction recorded in the
	// pass that decides the entry's status; null when the read failed or the
	// server holds no run history or workflow shape.
	PRLoop map[string]prLoopStationResponse `json:"pr_loop"`
}

// issueRows holds one issue's rows of the state route's row lists, as served.
type issueRows struct {
	running *runningEntryResponse
	retry   *retryEntryResponse
	budget  *budgetExhaustedResponse
	parked  *parkedEntryResponse
	pending []pendingReactionResponse // in served order
	between *stageHopResponse
}

// status returns the per-issue status the rows decide, or "" when they hold none.
func (r issueRows) status() string {
	switch {
	case r.running != nil:
		return statusRunning
	case r.retry != nil:
		return statusRetrying
	case r.budget != nil:
		return statusBudgetExhausted
	case r.parked != nil:
		return statusParked
	case len(r.pending) > 0:
		return statusPendingReactions
	case r.between != nil:
		return statusBetweenStages
	}
	return ""
}

// activeIssueOf copies the facts of the row that decides the issue's status.
// The rows must hold at least one row.
func activeIssueOf(rows issueRows) activeIssueResponse {
	status := rows.status()
	switch status {
	case statusRunning:
		r := rows.running
		return activeIssueResponse{
			IssueID: r.IssueID, IssueIdentifier: r.IssueIdentifier, DisplayIdentifier: r.DisplayIdentifier,
			IssueTitle: r.IssueTitle, IssueURL: r.IssueURL, Status: status, RuleName: r.RuleName,
			StagePosition: r.StagePosition, AgentKind: r.AgentKind, ConfiguredModel: r.ConfiguredModel,
			DispatchedAt: r.DispatchedAt, StatusText: r.StatusText, StatusTextFull: r.StatusTextFull,
		}
	case statusRetrying:
		r := rows.retry
		return activeIssueResponse{
			IssueID: r.IssueID, IssueIdentifier: r.IssueIdentifier, DisplayIdentifier: r.DisplayIdentifier,
			IssueTitle: r.IssueTitle, IssueURL: r.IssueURL, Status: status, RuleName: r.RuleName,
			StagePosition: r.StagePosition, AgentKind: r.AgentKind, ConfiguredModel: r.ConfiguredModel,
			DispatchedAt: r.DispatchedAt, StatusText: r.StatusText, StatusTextFull: r.StatusTextFull,
		}
	case statusBudgetExhausted:
		r := rows.budget
		return activeIssueResponse{
			IssueID: r.IssueID, IssueIdentifier: r.IssueIdentifier, DisplayIdentifier: r.DisplayIdentifier,
			IssueTitle: r.IssueTitle, IssueURL: r.IssueURL, Status: status, RuleName: r.RuleName,
			StagePosition: r.StagePosition, AgentKind: r.AgentKind, ConfiguredModel: r.ConfiguredModel,
			DispatchedAt: r.DispatchedAt, StatusText: r.StatusText, StatusTextFull: r.StatusTextFull,
		}
	case statusParked:
		r := rows.parked
		return activeIssueResponse{
			IssueID: r.IssueID, IssueIdentifier: r.IssueIdentifier, DisplayIdentifier: r.DisplayIdentifier,
			IssueTitle: r.IssueTitle, IssueURL: r.IssueURL, Status: status, RuleName: r.RuleName,
			StagePosition: r.StagePosition, AgentKind: r.AgentKind, ConfiguredModel: r.ConfiguredModel,
			DispatchedAt: r.DispatchedAt, StatusText: r.StatusText, StatusTextFull: r.StatusTextFull,
		}
	case statusPendingReactions:
		r := decidingPendingReaction(rows.pending)
		return activeIssueResponse{
			IssueID: r.IssueID, IssueIdentifier: r.IssueIdentifier, DisplayIdentifier: r.DisplayIdentifier,
			IssueTitle: r.IssueTitle, IssueURL: r.IssueURL, Status: status, RuleName: r.RuleName,
			StagePosition: r.StagePosition, AgentKind: r.AgentKind, ConfiguredModel: r.ConfiguredModel,
			ReactionKind: r.Kind, DispatchedAt: r.DispatchedAt, StatusText: r.StatusText, StatusTextFull: r.StatusTextFull,
		}
	default:
		r := rows.between
		return activeIssueResponse{
			IssueID: r.IssueID, IssueIdentifier: r.IssueIdentifier, DisplayIdentifier: r.DisplayIdentifier,
			IssueTitle: r.IssueTitle, IssueURL: r.IssueURL, Status: status, RuleName: r.RuleName,
			StagePosition: r.StagePosition, AgentKind: r.AgentKind, ConfiguredModel: r.ConfiguredModel,
			DispatchedAt: r.DispatchedAt, StatusText: r.StatusText, StatusTextFull: r.StatusTextFull,
		}
	}
}

// activeIssuesOf merges the row lists of resp into one entry per issue,
// ordered by the issue's recorded time, then by identifier, then by issue ID.
// An issue absent from order sorts before every issue with a recorded time.
// The result is never nil.
func activeIssuesOf(resp stateResponse, order map[string]time.Time) []activeIssueResponse {
	byIssue := make(map[string]*issueRows)
	rowsOf := func(issueID string) *issueRows {
		rows, ok := byIssue[issueID]
		if !ok {
			rows = &issueRows{}
			byIssue[issueID] = rows
		}
		return rows
	}
	for i := range resp.Running {
		rowsOf(resp.Running[i].IssueID).running = &resp.Running[i]
	}
	for i := range resp.Retrying {
		rowsOf(resp.Retrying[i].IssueID).retry = &resp.Retrying[i]
	}
	for i := range resp.BudgetExhausted {
		rowsOf(resp.BudgetExhausted[i].IssueID).budget = &resp.BudgetExhausted[i]
	}
	for i := range resp.Parked {
		rowsOf(resp.Parked[i].IssueID).parked = &resp.Parked[i]
	}
	for i := range resp.BetweenStages {
		rowsOf(resp.BetweenStages[i].IssueID).between = &resp.BetweenStages[i]
	}
	for _, row := range resp.PendingReactions {
		rows := rowsOf(row.IssueID)
		rows.pending = append(rows.pending, row)
	}

	entries := make([]activeIssueResponse, 0, len(byIssue))
	for _, rows := range byIssue {
		entries = append(entries, activeIssueOf(*rows))
	}
	slices.SortFunc(entries, func(a, b activeIssueResponse) int {
		return cmp.Or(
			order[a.IssueID].Compare(order[b.IssueID]),
			cmp.Compare(a.IssueIdentifier, b.IssueIdentifier),
			cmp.Compare(a.IssueID, b.IssueID),
		)
	})
	return entries
}
