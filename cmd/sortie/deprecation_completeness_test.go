package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/registry"
)

// deprecationCompletenessRegisteredKinds snapshots registry.Agents.Kinds()
// at package initialization, once main's own blank imports have run,
// so no sibling test file's own test-only registration leaks in.
var deprecationCompletenessRegisteredKinds = registry.Agents.Kinds()

// deprecationIssue names one kind whose declared Deprecation fails the
// Replacement rules, together with the reason.
type deprecationIssue struct {
	kind   string
	reason string
}

// checkDeprecationCoverage validates the Replacement rules of every
// kind in kinds whose lookup reports a non-nil Deprecation: Replacement
// must name a kind lookup reports registered, that kind's own
// Deprecation must be nil, and Replacement must differ from the
// declaring kind. A kind with a nil Deprecation, or one lookup does not
// report registered, draws no issue.
func checkDeprecationCoverage(kinds []string, lookup func(kind string) (registry.AgentMeta, bool)) []deprecationIssue {
	var issues []deprecationIssue
	for _, kind := range kinds {
		meta, ok := lookup(kind)
		if !ok || meta.Deprecation == nil {
			continue
		}

		replacement := meta.Deprecation.Replacement
		if replacement == kind {
			issues = append(issues, deprecationIssue{kind: kind, reason: fmt.Sprintf("names itself (%q) as its own replacement", replacement)})
			continue
		}

		replacementMeta, replacementOK := lookup(replacement)
		if !replacementOK {
			issues = append(issues, deprecationIssue{kind: kind, reason: fmt.Sprintf("names replacement %q, which is not registered", replacement)})
			continue
		}
		if replacementMeta.Deprecation != nil {
			issues = append(issues, deprecationIssue{kind: kind, reason: fmt.Sprintf("names replacement %q, which is itself deprecated", replacement)})
		}
	}
	return issues
}

// TestEveryDeprecatedKindNamesAValidReplacement enumerates every agent
// kind registry.Agents held once main's own blank imports had run and
// fails, naming the kind, when checkDeprecationCoverage reports an
// issue for it.
func TestEveryDeprecatedKindNamesAValidReplacement(t *testing.T) {
	t.Parallel()

	if len(deprecationCompletenessRegisteredKinds) == 0 {
		t.Fatal("registry.Agents.Kinds() returned no kinds, want at least the built-in adapters main.go blank-imports")
	}

	issues := checkDeprecationCoverage(deprecationCompletenessRegisteredKinds, registry.Agents.Meta)
	for _, issue := range issues {
		t.Errorf("agent kind %q: %s", issue.kind, issue.reason)
	}
}

// TestCheckDeprecationCoverage_NegativeControl proves the completeness
// mechanism itself can fail: a fixture kind naming an unregistered
// replacement, one naming a replacement that is itself deprecated, and
// one naming itself as its own replacement are each reported, while a
// non-deprecated kind and a correctly deprecated one are not.
func TestCheckDeprecationCoverage_NegativeControl(t *testing.T) {
	t.Parallel()

	lookup := func(kind string) (registry.AgentMeta, bool) {
		switch kind {
		case "not-deprecated-fixture":
			return registry.AgentMeta{}, true
		case "good-deprecated-fixture":
			return registry.AgentMeta{Deprecation: &registry.AgentDeprecation{Replacement: "not-deprecated-fixture"}}, true
		case "unregistered-replacement-fixture":
			return registry.AgentMeta{Deprecation: &registry.AgentDeprecation{Replacement: "does-not-exist-fixture"}}, true
		case "chained-deprecation-fixture":
			return registry.AgentMeta{Deprecation: &registry.AgentDeprecation{Replacement: "good-deprecated-fixture"}}, true
		case "self-referential-fixture":
			return registry.AgentMeta{Deprecation: &registry.AgentDeprecation{Replacement: "self-referential-fixture"}}, true
		default:
			return registry.AgentMeta{}, false
		}
	}

	tests := []struct {
		kind          string
		wantReasonSub string
	}{
		{kind: "not-deprecated-fixture"},
		{kind: "good-deprecated-fixture"},
		{kind: "unregistered-replacement-fixture", wantReasonSub: "not registered"},
		{kind: "chained-deprecation-fixture", wantReasonSub: "itself deprecated"},
		{kind: "self-referential-fixture", wantReasonSub: "own replacement"},
	}

	kinds := make([]string, len(tests))
	for i, tt := range tests {
		kinds[i] = tt.kind
	}
	byKind := make(map[string]string)
	for _, issue := range checkDeprecationCoverage(kinds, lookup) {
		byKind[issue.kind] = issue.reason
	}

	for _, tt := range tests {
		reason, reported := byKind[tt.kind]
		if tt.wantReasonSub == "" {
			if reported {
				t.Errorf("%s reported an issue, want none", tt.kind)
			}
			continue
		}
		if !reported {
			t.Errorf("%s reported no issue, want one", tt.kind)
			continue
		}
		if !strings.Contains(reason, tt.wantReasonSub) {
			t.Errorf("%s issue reason = %q, want it to contain %q", tt.kind, reason, tt.wantReasonSub)
		}
	}
}
