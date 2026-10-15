package githubpr

import (
	"github.com/sortie-ai/sortie/internal/registry"
)

// validateConfig checks github-pr-specific configuration constraints and
// returns diagnostics for the sortie validate pipeline. It does not
// construct an adapter instance or make network calls.
func validateConfig(fields registry.TrackerConfigFields) []registry.ValidationDiag {
	var diags []registry.ValidationDiag

	if _, _, ok := resolveEndpoint(fields.Endpoint); !ok {
		diags = append(diags, registry.ValidationDiag{
			Severity: "error",
			Check:    "tracker.endpoint.invalid",
			Message:  `tracker.endpoint must be an absolute http(s) URL with a host (e.g. "https://github.example.com/api/v3")`,
		})
	}
	diags = append(diags, registry.DiagOwnerRepoProject(fields.Project)...)
	diags = append(diags, registry.DiagStateLabelElements("tracker.active_states", fields.ActiveStates, registry.SeverityWarning)...)
	diags = append(diags, registry.DiagStateLabelElements("tracker.terminal_states", fields.TerminalStates, registry.SeverityWarning)...)
	diags = append(diags, registry.DiagStateOverlap(fields)...)

	return diags
}
