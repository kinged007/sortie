package orchestrator

import (
	"fmt"
	"strings"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/registry"
)

// ValidateConfigForPromotion checks whether a parsed config is safe to
// promote as the effective config for ongoing orchestration operations
// (reconciliation, retry scheduling, worker exit handling). Returns an
// error if the config would cause incorrect behavior in these paths.
//
// meta is the registration of cfg.Tracker.Kind; an unregistered kind passes
// the zero value. A tracker that requires terminal states is refused an
// empty tracker.terminal_states, and every tracker is refused a blank entry
// in either state list, because a blank state would match an issue that
// reads no state. The last two rules return a [*config.ConfigError].
//
// It is compatible with [workflow.WithValidateFunc] once bound to a meta
// lookup, so that [workflow.Manager] can gate config promotion on
// domain-level safety rules without coupling to orchestration internals.
func ValidateConfigForPromotion(cfg config.ServiceConfig, meta registry.TrackerMeta) error {
	if meta.RequiresTerminalStates && len(cfg.Tracker.TerminalStates) == 0 {
		return &config.ConfigError{
			Field: "tracker.terminal_states",
			Message: fmt.Sprintf("must name at least one state for tracker kind %q: a closed issue reads as its first entry",
				cfg.Tracker.Kind),
		}
	}
	if len(cfg.Tracker.ActiveStates) == 0 && len(cfg.Tracker.TerminalStates) == 0 {
		return fmt.Errorf("tracker.active_states and tracker.terminal_states are both empty; at least one must be configured")
	}
	if err := checkNoBlankState("tracker.active_states", cfg.Tracker.ActiveStates); err != nil {
		return err
	}
	return checkNoBlankState("tracker.terminal_states", cfg.Tracker.TerminalStates)
}

func checkNoBlankState(field string, states []string) error {
	for i, state := range states {
		if strings.TrimSpace(state) == "" {
			return &config.ConfigError{
				Field:   fmt.Sprintf("%s[%d]", field, i),
				Message: "needs a state name with a character other than white space",
			}
		}
	}
	return nil
}
