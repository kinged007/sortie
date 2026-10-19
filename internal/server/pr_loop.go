package server

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/sortie-ai/sortie/internal/orchestrator"
)

const (
	prLoopEscalated  = "escalated"
	prLoopActed      = "acted"
	prLoopQuiet      = "quiet"
	prLoopUnrecorded = "unrecorded"
)

// prLoopStationResponse is what one configured pull request reaction
// recorded in a pass. Continuations counts continuations scheduled, so a fix
// that a budget hold kept from running still counts.
type prLoopStationResponse struct {
	Result        string `json:"result"` // "escalated", "acted", "quiet", or "unrecorded"
	Continuations int    `json:"continuations"`
}

// prLoopOf builds the pr_loop member of one pass: one entry per reaction key.
// Kinds fold into the key they are configured under, and a pass that is not
// recorded reads unrecorded at every key.
func prLoopOf(activity PassActivity, reactionKeys []string) map[string]prLoopStationResponse {
	out := make(map[string]prLoopStationResponse, len(reactionKeys))
	for _, key := range reactionKeys {
		entry := prLoopStationResponse{Result: prLoopUnrecorded}
		if activity.Recorded {
			entry = prLoopEntryOf(activity.Kinds, key)
		}
		out[key] = entry
	}
	return out
}

func prLoopEntryOf(kinds map[string]KindActivity, key string) prLoopStationResponse {
	var watched, acted, escalated bool
	var continuations int
	for kind, activity := range kinds {
		if orchestrator.ReactionConfigKey(kind) != key {
			continue
		}
		watched = watched || activity.Watched
		acted = acted || activity.Acted
		escalated = escalated || activity.Escalated
		continuations += activity.Continuations
	}

	entry := prLoopStationResponse{Result: prLoopUnrecorded, Continuations: continuations}
	switch {
	case escalated:
		entry.Result = prLoopEscalated
	case acted || continuations > 0:
		entry.Result = prLoopActed
	case watched:
		entry.Result = prLoopQuiet
	}
	return entry
}

// reactionKeysOf lists the reactions keys the shape holds, in the order the
// workflow reference gives them.
func reactionKeysOf(r orchestrator.ShapeReactions) []string {
	keys := make([]string, 0, 7)
	for _, member := range []struct {
		key     string
		present bool
	}{
		{"ci_failure", r.CIFailure != nil},
		{"review_comments", r.ReviewComments != nil},
		{"bot_review", r.BotReview != nil},
		{"merge_conflicts", r.MergeConflicts != nil},
		{"auto_merge", r.AutoMerge != nil},
		{"merge_completion", r.MergeCompletion != nil},
		{"label_commands", r.LabelCommands != nil},
	} {
		if member.present {
			keys = append(keys, member.key)
		}
	}
	return keys
}

// passOfLineage names the pass a stage lineage belongs to.
func passOfLineage(issueID string, lineage orchestrator.StageLineage) PassRef {
	return PassRef{IssueID: issueID, ChainID: lineage.ChainID, DispatchedAt: lineage.DispatchedAt}
}

// passOfRun names the pass a run row belongs to. A dispatch time that does
// not parse leaves the pass undated.
func passOfRun(row RunHistoryEntry) PassRef {
	dispatchedAt, _ := time.Parse(time.RFC3339Nano, row.DispatchedAt)
	return PassRef{IssueID: row.IssueID, ChainID: row.ChainID, DispatchedAt: dispatchedAt}
}

// activePasses names, for each issue the snapshot lists, the pass of the row
// that decides its status. The rows are taken in the order the per-issue
// status ranks them; a pending reaction decides by its first row whose
// escalation stands, else its first row.
func activePasses(snap orchestrator.RuntimeSnapshotResult) map[string]PassRef {
	passes := make(map[string]PassRef)
	add := func(ref PassRef) {
		if _, decided := passes[ref.IssueID]; !decided {
			passes[ref.IssueID] = ref
		}
	}
	for _, e := range snap.Running {
		add(passOfLineage(e.IssueID, e.StageLineage))
	}
	for _, e := range snap.Retrying {
		add(passOfLineage(e.IssueID, e.StageLineage))
	}
	for _, e := range snap.BudgetExhausted {
		add(passOfLineage(e.IssueID, e.StageLineage))
	}
	for _, e := range snap.Parked {
		add(PassRef{IssueID: e.IssueID, ChainID: e.ChainID, DispatchedAt: e.DispatchedAt})
	}

	deciding := make(map[string]orchestrator.SnapshotPendingReaction)
	for _, e := range snap.PendingReactions {
		held, ok := deciding[e.IssueID]
		if !ok || (held.EscalatedAt == nil && e.EscalatedAt != nil) {
			deciding[e.IssueID] = e
		}
	}
	for _, e := range deciding {
		add(PassRef{IssueID: e.IssueID, ChainID: e.ChainID, DispatchedAt: e.DispatchedAt})
	}

	for _, e := range snap.BetweenStages {
		add(PassRef{IssueID: e.IssueID, ChainID: e.ChainID, DispatchedAt: e.DispatchedAt})
	}
	return passes
}

// prLoops holds the pr_loop of every pass a request serves. The nil value
// holds none, so every lookup serves null.
type prLoops map[PassKey]map[string]prLoopStationResponse

// of returns the pr_loop of ref's pass, or nil when the request holds none.
func (l prLoops) of(ref PassRef) map[string]prLoopStationResponse {
	return l[PassKey{IssueID: ref.IssueID, ChainID: ref.ChainID}]
}

// readPRLoops reads the activity of passes in one PassActivities call. It
// returns nil loops when the server holds no run history or the request read
// no workflow shape, and the read error when the read fails.
func (s *Server) readPRLoops(ctx context.Context, shape *orchestrator.WorkflowShape, passes []PassRef) (prLoops, error) {
	if s.runHistory == nil || shape == nil {
		return nil, nil
	}

	distinct := make([]PassRef, 0, len(passes))
	seen := make(map[PassKey]struct{}, len(passes))
	for _, ref := range passes {
		key := PassKey{IssueID: ref.IssueID, ChainID: ref.ChainID}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		distinct = append(distinct, ref)
	}

	loops := make(prLoops, len(distinct))
	if len(distinct) == 0 {
		return loops, nil
	}
	activities, err := s.runHistory.PassActivities(ctx, distinct)
	if err != nil {
		return nil, fmt.Errorf("read reaction activity: %w", err)
	}
	keys := reactionKeysOf(shape.Reactions)
	for key := range seen {
		loops[key] = prLoopOf(activities[key], keys)
	}
	return loops, nil
}

// attachActivePRLoops sets pr_loop on every active entry of resp. A failed
// read leaves every entry's pr_loop null and the response unchanged
// otherwise.
func (s *Server) attachActivePRLoops(r *http.Request, resp *stateResponse, snap orchestrator.RuntimeSnapshotResult, shape *orchestrator.WorkflowShape) {
	passes := activePasses(snap)
	loops, err := s.readPRLoops(r.Context(), shape, slices.Collect(maps.Values(passes)))
	if err != nil {
		s.logger.Warn("failed to read reaction activity",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Any("error", err),
		)
		return
	}
	for i := range resp.Active {
		resp.Active[i].PRLoop = loops.of(passes[resp.Active[i].IssueID])
	}
}

// attachIssuePRLoop sets pr_loop on detail from the pass of the row that
// decides a live issue's status, or else from newest, the issue's newest run.
// Without either it leaves pr_loop null.
func (s *Server) attachIssuePRLoop(ctx context.Context, detail *issueDetailResponse, snap orchestrator.RuntimeSnapshotResult, shape *orchestrator.WorkflowShape, newest *RunHistoryEntry) error {
	ref, live := activePasses(snap)[detail.IssueID]
	if !live {
		if newest == nil {
			return nil
		}
		ref = passOfRun(*newest)
	}
	loops, err := s.readPRLoops(ctx, shape, []PassRef{ref})
	if err != nil {
		return err
	}
	detail.PRLoop = loops.of(ref)
	return nil
}

// attachRunPRLoops sets pr_loop on each run of runs from the pass of the
// matching row of rows.
func attachRunPRLoops(runs []runResponse, rows []RunHistoryEntry, loops prLoops) {
	for i := range runs {
		runs[i].PRLoop = loops.of(passOfRun(rows[i]))
	}
}
