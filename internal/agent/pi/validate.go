package pi

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/typeutil"
)

// validateConfig checks pi-specific configuration constraints and
// returns diagnostics for the sortie validate pipeline. It does not
// construct an adapter instance or launch a subprocess. It shares the
// overlap check with [NewPiAdapter], which reaches it through
// [checkCrossField], so the constructor's refusal and the offline
// verdict report that fault identically.
func validateConfig(fields registry.AgentConfigFields) []registry.ValidationDiag {
	var diags []registry.ValidationDiag

	if _, fault := parsePassthroughConfig(fields.Passthrough); fault != nil {
		diags = append(diags, registry.ValidationDiag{
			Severity: "error",
			Check:    "pi." + fault.Key + ".wrong_type",
			Message:  fault.Error(),
		})
	}

	diags = append(diags, validateToolOverlap(fields.Passthrough)...)

	return diags
}

// validateToolOverlap reports an error when allowed_tools and
// denied_tools name at least one of the same tools, mirroring the check
// [checkCrossField] used to enforce inline at construction.
func validateToolOverlap(passthrough map[string]any) []registry.ValidationDiag {
	message := overlapMessage(
		typeutil.ExtractStringSlice(passthrough["allowed_tools"]),
		typeutil.ExtractStringSlice(passthrough["denied_tools"]),
	)
	if message == "" {
		return nil
	}

	return []registry.ValidationDiag{{
		Severity: "error",
		Check:    "pi.allowed_tools.overlap",
		Message:  message,
	}}
}

// overlapMessage returns the byte-identical message both [validateConfig]
// and [NewPiAdapter] report when allowed and denied name at least
// one common tool, or "" when they do not overlap.
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
