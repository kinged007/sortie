package issuekit

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/sortie-ai/sortie/internal/domain"
)

// LabelStates is one snapshot of the state vocabulary a label-modelled
// tracker derives issue state from. Every entry is lowercased, and Handoff
// and InProgress are also trimmed, by the caller before construction.
type LabelStates struct {
	Active     []string
	Terminal   []string
	Handoff    string
	InProgress string
}

// Derive returns the state of an issue carrying labels and the configured
// states those labels name, in precedence order: each terminal state, then
// the handoff state, then each active state. A label names a state when the
// two compare equal ignoring letter case.
//
// An open issue reads the first carried state, or the empty string when it
// carries none. A closed issue reads the first carried terminal state, else
// the first configured terminal state, else the empty string. The empty
// string is the no-state reading and never equals a configured state.
func (s LabelStates) Derive(labels []string, closed bool) (state string, carried []string) {
	terminals := 0
	for _, t := range s.Terminal {
		if carriesState(labels, t) && !slices.Contains(carried, t) {
			carried = append(carried, t)
			terminals++
		}
	}
	for _, name := range s.nonTerminalStates() {
		if carriesState(labels, name) && !slices.Contains(carried, name) {
			carried = append(carried, name)
		}
	}

	if closed {
		if terminals > 0 {
			return carried[0], carried
		}
		if len(s.Terminal) > 0 {
			return s.Terminal[0], carried
		}
		return "", carried
	}
	if len(carried) == 0 {
		return "", carried
	}
	return carried[0], carried
}

// Candidates splits open into the issues whose state is a configured active
// state, in listing order, and a count of the issues that read no state.
func (s LabelStates) Candidates(open []domain.Issue) (kept []domain.Issue, unstated int) {
	kept = make([]domain.Issue, 0, len(open))
	for _, issue := range open {
		switch {
		case issue.State == "":
			unstated++
		case slices.Contains(s.Active, issue.State):
			kept = append(kept, issue)
		}
	}
	return kept, unstated
}

// MissingActiveLabels returns the active states, minus the in-progress
// state, that no label of catalog names, in active-state order.
func (s LabelStates) MissingActiveLabels(catalog []string) []string {
	var missing []string
	for _, name := range s.checkedActive() {
		if !carriesState(catalog, name) {
			missing = append(missing, name)
		}
	}
	return missing
}

// checkedActive returns the active states the missing-label check covers:
// Sortie writes the in-progress label itself, so a missing one surfaces as a
// transition error rather than a silent gap.
func (s LabelStates) checkedActive() []string {
	var checked []string
	for _, name := range s.Active {
		if strings.EqualFold(name, s.InProgress) || slices.Contains(checked, name) {
			continue
		}
		checked = append(checked, name)
	}
	return checked
}

func (s LabelStates) nonTerminalStates() []string {
	if s.Handoff == "" {
		return s.Active
	}
	return append([]string{s.Handoff}, s.Active...)
}

func carriesState(labels []string, state string) bool {
	for _, label := range labels {
		if SameLabel(label, state) {
			return true
		}
	}
	return false
}

// LabelStateModel applies a [StateSource]'s vocabulary to a label-modelled
// tracker and reports what an operator needs to hear once rather than on every
// poll. It is safe for concurrent use.
type LabelStateModel struct {
	source *StateSource
	log    *slog.Logger

	mu             sync.Mutex
	reportedLabels map[string]string
	unstated       int
	checkedActive  []string
	activeChecked  bool
}

// NewLabelStateModel returns a model that reads its vocabulary from source.
// A nil log falls back to [slog.Default].
func NewLabelStateModel(source *StateSource, log *slog.Logger) *LabelStateModel {
	return &LabelStateModel{
		source:         source,
		log:            log,
		reportedLabels: make(map[string]string),
	}
}

// Snapshot returns the source's current vocabulary, lowercased and with the
// handoff and in-progress states trimmed. An adapter takes one snapshot per
// operation and passes it to every method below.
func (m *LabelStateModel) Snapshot() LabelStates {
	current := m.source.TrackerStates()
	return LabelStates{
		Active:     lowerAll(current.Active),
		Terminal:   lowerAll(current.Terminal),
		Handoff:    strings.ToLower(strings.TrimSpace(current.Handoff)),
		InProgress: strings.ToLower(strings.TrimSpace(current.InProgress)),
	}
}

// Derive returns the state [LabelStates.Derive] reads for the issue. An open
// issue carrying labels of more than one configured state draws one WARN per
// distinct label set.
func (m *LabelStateModel) Derive(s LabelStates, issueIdentifier string, labels []string, closed bool) string {
	state, carried := s.Derive(labels, closed)
	if m.firstReport(issueIdentifier, carried, closed) {
		m.logger().Warn("issue carries more than one state label",
			slog.String("issue_identifier", issueIdentifier),
			slog.Any("matched_labels", carried),
			slog.String("state", state))
	}
	return state
}

// Candidates returns the open issues in a configured active state and logs
// the number skipped for reading no state when it differs from the previous
// call's.
func (m *LabelStateModel) Candidates(s LabelStates, open []domain.Issue) []domain.Issue {
	kept, unstated := s.Candidates(open)

	m.mu.Lock()
	changed := unstated != m.unstated
	m.unstated = unstated
	m.mu.Unlock()

	if changed {
		m.logger().Info("skipped open issues without a state label", slog.Int("count", unstated))
	}
	return kept
}

// CheckActiveLabels warns about each configured active label that read does
// not return. It reads only when the checked set differs from the last one
// checked, a failed read counts as a check, and no outcome is an error.
func (m *LabelStateModel) CheckActiveLabels(ctx context.Context, s LabelStates, read func(context.Context) ([]string, error)) {
	if !m.claimActiveCheck(s.checkedActive()) {
		return
	}

	catalog, err := read(ctx)
	if err != nil {
		m.logger().Warn("failed to read tracker labels for the active label check",
			slog.Any("error", err))
		return
	}
	for _, label := range s.MissingActiveLabels(catalog) {
		m.logger().Warn("configured active label missing from tracker labels",
			slog.String("label", label))
	}
}

// firstReport reports whether this label set is new for the issue and
// updates the memory: an issue observed with at most one state label is
// forgotten.
func (m *LabelStateModel) firstReport(issueIdentifier string, carried []string, closed bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if closed || len(carried) < 2 {
		delete(m.reportedLabels, issueIdentifier)
		return false
	}
	key := strings.Join(carried, "\x00")
	if m.reportedLabels[issueIdentifier] == key {
		return false
	}
	m.reportedLabels[issueIdentifier] = key
	return true
}

// claimActiveCheck reports whether the caller should read the catalog for
// checked, recording it as the last checked set.
func (m *LabelStateModel) claimActiveCheck(checked []string) bool {
	checked = sortedCopy(checked)

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeChecked && slices.Equal(m.checkedActive, checked) {
		return false
	}
	m.checkedActive = checked
	m.activeChecked = true
	return len(checked) > 0
}

func (m *LabelStateModel) logger() *slog.Logger {
	if m.log == nil {
		return slog.Default()
	}
	return m.log
}

func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}
