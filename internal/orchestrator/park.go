package orchestrator

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/persistence"
)

// Park reason and unpark trigger values, shared by both parking triggers
// and by every release path.
const (
	parkReasonAgentBlocked   = "agent_blocked"
	parkReasonHandoffAbsence = "handoff_absence"

	unparkTriggerStateChanged     = "state_changed"
	unparkTriggerLabelRemoved     = "label_removed"
	unparkTriggerEvidenceObserved = "evidence_observed"
)

// ParkStore is the persistence contract [parkIssue] and [unparkIssue] need.
type ParkStore interface {
	UpsertParkedIssue(ctx context.Context, entry persistence.ParkedIssue) error
	DeleteParkedIssue(ctx context.Context, issueID string) error
	DeleteRetryEntry(ctx context.Context, issueID string) error
	ResetHandoffAbsenceSequence(ctx context.Context, issueID string) error
	DeleteStageHop(ctx context.Context, issueID string) error
}

// parkObserverStore widens [ParkStore] with the one method the release pass
// needs to confirm a parking label. It stays off ParkStore itself, because
// only the release pass, running on the orchestrator's own store, ever
// reaches it.
type parkObserverStore interface {
	ParkStore
	MarkParkedIssueLabelApplied(ctx context.Context, issueID string) error
}

// parkedRun is the run a park follows: its dispatch rule, its frozen stage
// lineage (pass dispatch time included), its agent kind, and its configured
// model. The zero value records no run.
type parkedRun struct {
	RuleName        string
	StageLineage    StageLineage
	AgentKind       string
	ConfiguredModel string
}

// parkedRunOf returns the run row describes: AgentKind from AgentAdapter,
// StageLineage through [stageLineageOf].
func parkedRunOf(row persistence.RunHistory) parkedRun {
	return parkedRun{
		RuleName:        row.RuleName,
		StageLineage:    stageLineageOf(row),
		AgentKind:       row.AgentAdapter,
		ConfiguredModel: row.ConfiguredModel,
	}
}

// parkIssueParams carries the inputs of one park.
type parkIssueParams struct {
	IssueID       string
	Identifier    string
	DisplayID     string
	IssueIdentity IssueIdentity // the issue as read; zero when unread
	ObservedState string        // tracker state to record as parked_state
	Reason        string
	Label         string // resolved parking label; empty disables the label write
	Absences      int    // log attribute only; omitted from the record when zero
	Ceiling       int    // log attribute only; omitted from the record when zero
	Run           parkedRun

	Store          ParkStore
	TrackerAdapter domain.TrackerAdapter // nil skips the label write
	Metrics        domain.Metrics
	Logger         *slog.Logger
	Ctx            context.Context
}

// parkIssue records the park, releases the claim, cancels and deletes any
// pending retry, resets the issue's stage hop count, and starts the detached
// parking-label write.
func parkIssue(state *State, params parkIssueParams) {
	ctx := params.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	log := params.Logger
	if log == nil {
		log = slog.Default()
	}

	CancelRetry(state, params.IssueID)
	delete(state.Claimed, params.IssueID)

	// An issue parked again still carries the label of its earlier park,
	// since an unpark never removes it.
	identity := params.IssueIdentity
	identity.Labels = labelsWithout(identity.Labels, params.Label)

	parkedAt := time.Now().UTC()
	entry := &ParkedEntry{
		Identifier:    params.Identifier,
		DisplayID:     params.DisplayID,
		IssueIdentity: identity,
		Reason:        params.Reason,
		ParkedState:   params.ObservedState,
		Label:         params.Label,
		LabelApplied:  false,
		ParkedAt:      parkedAt,

		RuleName:        params.Run.RuleName,
		StageLineage:    params.Run.StageLineage,
		AgentKind:       params.Run.AgentKind,
		ConfiguredModel: params.Run.ConfiguredModel,
	}
	state.Parked[params.IssueID] = entry

	if err := params.Store.UpsertParkedIssue(ctx, parkedIssueRow(params.IssueID, entry)); err != nil {
		log.Error("failed to persist park record", slog.Any("error", err))
	}
	if err := params.Store.DeleteRetryEntry(ctx, params.IssueID); err != nil {
		log.Error("failed to delete retry entry after park", slog.Any("error", err))
	}
	resetStageHop(ctx, state, params.Store, params.IssueID, stageResetParked, log)

	params.Metrics.IncIssueParks(params.Reason)

	attrs := []slog.Attr{
		slog.String("reason", params.Reason),
		slog.String("parked_state", params.ObservedState),
		slog.String("label", params.Label),
	}
	if params.Absences != 0 {
		attrs = append(attrs, slog.Int("consecutive_absences", params.Absences))
	}
	if params.Ceiling != 0 {
		attrs = append(attrs,
			slog.Int("absence_ceiling", params.Ceiling),
			slog.String("ceiling_setting", "agent.max_consecutive_absences"),
		)
	}
	log.LogAttrs(ctx, slog.LevelWarn, "issue parked", attrs...)

	if params.TrackerAdapter == nil || params.Label == "" {
		return
	}

	issueID := params.IssueID
	label := params.Label
	tracker := params.TrackerAdapter
	state.TrackerOpsWg.Go(func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()

		if err := tracker.AddLabel(dctx, issueID, label); err != nil {
			log.Warn("park label write failed", slog.Any("error", err))
		}
	})
}

// unparkIssue lifts the park, deletes the persisted row, and resets the
// issue's stage hop count. It is a no-op when issueID is absent from
// state.Parked.
func unparkIssue(ctx context.Context, state *State, issueID, trigger string, store ParkStore, log *slog.Logger) {
	if ctx == nil {
		ctx = context.Background()
	}
	if log == nil {
		log = slog.Default()
	}

	entry, ok := state.Parked[issueID]
	if !ok {
		return
	}
	delete(state.Parked, issueID)

	if err := store.DeleteParkedIssue(ctx, issueID); err != nil {
		log.Error("failed to delete park record", slog.Any("error", err))
	}
	if err := store.ResetHandoffAbsenceSequence(ctx, issueID); err != nil {
		log.Error("failed to reset handoff absence sequence after unpark", slog.Any("error", err))
	}
	resetStageHop(ctx, state, store, issueID, stageResetUnparked, log)

	log.Info("issue unparked",
		slog.String("trigger", trigger),
		slog.String("reason", entry.Reason),
	)
}

// PopulateParked loads persisted park records into state.Parked. Called
// during startup recovery, before the event loop starts, beside
// [PopulateRetries].
func PopulateParked(state *State, rows []persistence.ParkedIssue, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}

	for _, row := range rows {
		if row.IssueID == "" {
			log.Warn("skipping malformed park record")
			continue
		}

		parkedAt, err := time.Parse(time.RFC3339, row.ParkedAt)
		if err != nil {
			parkedAt = time.Time{}
		}

		state.Parked[row.IssueID] = &ParkedEntry{
			Identifier: row.Identifier,
			DisplayID:  row.DisplayID,
			IssueIdentity: IssueIdentity{
				Title:     row.IssueTitle,
				URL:       row.IssueURL,
				IssueType: row.IssueType,
				Labels:    row.IssueLabels,
			},
			Reason:       row.Reason,
			ParkedState:  row.ParkedState,
			Label:        row.Label,
			LabelApplied: row.LabelApplied,
			ParkedAt:     parkedAt,

			RuleName: row.RuleName,
			StageLineage: StageLineage{
				PreviousRule:    row.StagePrevious,
				PreviousOutcome: row.StagePreviousOutcome,
				ChainID:         row.ChainID,
				DispatchedAt:    parseDispatchedAt(row.DispatchedAt),
			},
			AgentKind:       row.AgentKind,
			ConfiguredModel: row.ConfiguredModel,
		}
	}
}

// observeParkedState evaluates the state-change release rule for one
// parked issue against an observed tracker state and reports whether the
// issue is still parked afterward. An empty observed state is ignored. An
// empty entry.ParkedState is backfilled from observed without releasing.
// A non-empty entry.ParkedState that differs from observed, compared
// case-insensitively, unparks the issue with trigger "state_changed".
func observeParkedState(ctx context.Context, state *State, store parkObserverStore, log *slog.Logger, entry *ParkedEntry, issueID, observed string) (stillParked bool) {
	if observed == "" {
		return true
	}
	if entry.ParkedState == "" {
		entry.ParkedState = observed
		if err := store.UpsertParkedIssue(ctx, parkedIssueRow(issueID, entry)); err != nil {
			log.Error("failed to persist park state backfill", slog.Any("error", err))
		}
		return true
	}
	if !strings.EqualFold(observed, entry.ParkedState) {
		unparkIssue(ctx, state, issueID, unparkTriggerStateChanged, store, log)
		return false
	}
	return true
}

// observeParkedLabels evaluates the label-confirmation and label-removal
// release rules for one parked issue against an observed label set. A
// parking label observed while unconfirmed sets and persists
// LabelApplied. A parking label observed absent while confirmed unparks
// the issue with trigger "label_removed". A parking label observed absent
// while unconfirmed leaves the park unchanged.
func observeParkedLabels(ctx context.Context, state *State, store parkObserverStore, log *slog.Logger, entry *ParkedEntry, issueID string, labels []string) {
	hasLabel := slices.ContainsFunc(labels, func(label string) bool {
		return strings.EqualFold(label, entry.Label)
	})

	if hasLabel && !entry.LabelApplied {
		entry.LabelApplied = true
		if err := store.MarkParkedIssueLabelApplied(ctx, issueID); err != nil {
			log.Error("failed to persist park label confirmation", slog.Any("error", err))
		}
		return
	}
	if !hasLabel && entry.LabelApplied {
		unparkIssue(ctx, state, issueID, unparkTriggerLabelRemoved, store, log)
	}
}

// labelsWithout returns a copy of labels without each label that
// [strings.EqualFold] matches to drop, comparing as [observeParkedLabels]
// does. A nil labels stays nil.
func labelsWithout(labels []string, drop string) []string {
	if labels == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(labels), func(label string) bool {
		return strings.EqualFold(label, drop)
	})
}

// parkedIssueRow builds the persisted row for the given runtime park entry.
func parkedIssueRow(issueID string, entry *ParkedEntry) persistence.ParkedIssue {
	return persistence.ParkedIssue{
		IssueID:      issueID,
		Identifier:   entry.Identifier,
		DisplayID:    entry.DisplayID,
		Reason:       entry.Reason,
		ParkedState:  entry.ParkedState,
		Label:        entry.Label,
		LabelApplied: entry.LabelApplied,
		ParkedAt:     entry.ParkedAt.Format(time.RFC3339),
		IssueTitle:   entry.IssueIdentity.Title,
		IssueURL:     entry.IssueIdentity.URL,
		IssueType:    entry.IssueIdentity.IssueType,
		IssueLabels:  entry.IssueIdentity.Labels,

		RuleName:             entry.RuleName,
		AgentKind:            entry.AgentKind,
		ConfiguredModel:      entry.ConfiguredModel,
		StagePrevious:        entry.StageLineage.PreviousRule,
		StagePreviousOutcome: entry.StageLineage.PreviousOutcome,
		ChainID:              entry.StageLineage.ChainID,
		DispatchedAt:         formatDispatchedAt(entry.StageLineage.DispatchedAt),
	}
}
