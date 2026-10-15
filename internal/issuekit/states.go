package issuekit

import (
	"sync/atomic"

	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/typeutil"
)

// StateSource hands a tracker adapter the workflow's state vocabulary. An
// adapter embeds a *StateSource, which gives it the SetTrackerStates method
// of [domain.TrackerAdapter]. A StateSource is safe for concurrent use.
type StateSource struct {
	initial domain.TrackerStates
	current atomic.Pointer[func() domain.TrackerStates]
}

// NewStateSource returns a StateSource that reports initial until a function
// is installed.
func NewStateSource(initial domain.TrackerStates) *StateSource {
	return &StateSource{initial: initial}
}

// SetTrackerStates installs the function TrackerStates calls. A nil function
// restores the initial vocabulary.
func (s *StateSource) SetTrackerStates(current func() domain.TrackerStates) {
	if current == nil {
		s.current.Store(nil)
		return
	}
	s.current.Store(&current)
}

// TrackerStates returns the installed function's result, or the initial
// vocabulary when none is installed.
func (s *StateSource) TrackerStates() domain.TrackerStates {
	if current := s.current.Load(); current != nil {
		return (*current)()
	}
	return s.initial
}

// TrackerStatesFromConfig reads the state vocabulary from an adapter's
// configuration map and applies no default. A handoff_state or
// in_progress_state that is not a string returns a [*domain.TrackerError]
// of kind [domain.ErrTrackerPayload].
func TrackerStatesFromConfig(config map[string]any) (domain.TrackerStates, error) {
	handoff, fault := typeutil.StringField(config, "handoff_state")
	if fault != nil {
		return domain.TrackerStates{}, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: fault.Error()}
	}
	inProgress, fault := typeutil.StringField(config, "in_progress_state")
	if fault != nil {
		return domain.TrackerStates{}, &domain.TrackerError{Kind: domain.ErrTrackerPayload, Message: fault.Error()}
	}
	return domain.TrackerStates{
		Active:     typeutil.ExtractStringSlice(config["active_states"]),
		Terminal:   typeutil.ExtractStringSlice(config["terminal_states"]),
		Handoff:    handoff,
		InProgress: inProgress,
	}, nil
}
