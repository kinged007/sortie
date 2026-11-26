package main

import (
	"testing"

	"github.com/sortie-ai/sortie/internal/registry"
)

const (
	usageLimitCompletenessImportPath     = "github.com/sortie-ai/sortie/internal/agent/agenttest"
	usageLimitCompletenessAssertFuncName = "AssertUsageLimitRefusal"
)

func TestEveryUsageLimitRecognizingKindHasRefusalCoverage(t *testing.T) {
	t.Parallel()

	var kinds []string
	for _, kind := range mcpCompletenessRegisteredKinds {
		meta, ok := registry.Agents.Meta(kind)
		if ok && meta.RequiresCommand && meta.UsageLimitRecognition == registry.UsageLimitRecognized {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		t.Fatal("no registered agent kind that requires a command declares usage-limit recognition, want at least one (claude-code, codex)")
	}

	missing := kindsMissingCoverage(kinds, mcpCompletenessAgentRoot, usageLimitCompletenessImportPath, usageLimitCompletenessAssertFuncName)
	if len(missing) != 0 {
		t.Errorf("agent kind(s) %v recognize a usage-limit refusal but have no test file calling agenttest.AssertUsageLimitRefusal against their own package", missing)
	}
}
