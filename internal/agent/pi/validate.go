package pi

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sortie-ai/sortie/internal/registry"
)

// validateConfig checks pi-specific configuration constraints and
// returns diagnostics for the sortie validate pipeline. It does not
// construct an adapter instance or launch a subprocess. It reports
// exactly what [NewPiAdapter] refuses on, because both call
// [parsePassthroughConfig]: one implementation, so the constructor's
// refusal and the offline verdict can never disagree.
func validateConfig(fields registry.AgentConfigFields) []registry.ValidationDiag {
	_, faults := parsePassthroughConfig(fields.Passthrough)

	diags := make([]registry.ValidationDiag, 0, len(faults))
	for _, f := range faults {
		diags = append(diags, registry.ValidationDiag{
			Severity: "error",
			Check:    f.Check,
			Message:  f.Message,
		})
	}
	return diags
}

// overlapMessage returns the message reported when allowed_tools and
// denied_tools name at least one of the same tools, or "" when they do
// not overlap.
func overlapMessage(allowed, denied []string) string {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}

	var conflicts []string
	for _, key := range denied {
		if _, ok := allowedSet[key]; ok {
			conflicts = append(conflicts, key)
		}
	}
	if len(conflicts) == 0 {
		return ""
	}

	slices.Sort(conflicts)
	return fmt.Sprintf("allowed_tools and denied_tools overlap: %s", strings.Join(conflicts, ", "))
}
