package clientprotocol

import (
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/agent/clientprotocol/usagesource"
	"github.com/sortie-ai/sortie/internal/registry"
)

const (
	scriptedModelTest      = "TestIntegration_ScriptedModel"
	contextScenarioSubtest = "credential_verification_context"
)

func filterSelects(filter string, path ...string) (bool, error) {
	elements := strings.Split(filter, "/")
	for level, name := range path {
		if level >= len(elements) {
			break
		}
		matched, err := regexp.MatchString(elements[level], name)
		if err != nil || !matched {
			return false, err
		}
	}
	return true, nil
}

func rowRunsContextScenario(row map[string]string) (bool, error) {
	path := []string{scriptedModelTest, contextScenarioSubtest}
	selected, err := filterSelects(row["run_filter"], path...)
	if err != nil || !selected || row["skip_filter"] == "" {
		return selected, err
	}
	skipped, err := filterSelects(row["skip_filter"], path...)
	return !skipped, err
}

func rowRunsOnlyContextScenario(row map[string]string) bool {
	selected, err := rowRunsContextScenario(row)
	return err == nil && selected && strings.Contains(row["run_filter"], "/")
}

func dropsAContextComponent(context registry.VerificationContext) bool {
	return !context.Keeps(registry.VerificationKeepsAllTools) ||
		!context.Keeps(registry.VerificationKeepsProjectInstructions) ||
		!context.Keeps(registry.VerificationKeepsUserInstructions) ||
		!context.Keeps(registry.VerificationKeepsSystemPrompt)
}

func rowProvesAddition(t *testing.T, row map[string]string, want reflect.Type) bool {
	t.Helper()

	runs, err := rowRunsContextScenario(row)
	if err != nil {
		t.Fatalf("row %q: compile run_filter %q or skip_filter %q: %v", row["name"], row["run_filter"], row["skip_filter"], err)
	}
	words := strings.Fields(row["clientprotocol_command"])
	reachesFakeModel := row["clientprotocol_model_base_url_env"] != "" || row["clientprotocol_model_document_path"] != ""
	if !runs || len(words) == 0 || row["credential_env"] == "" || !reachesFakeModel {
		return false
	}

	addition, _, claimed := usagesource.ClaimVerification(agentcore.LaunchTarget{Command: words[0], Args: words[1:]}, nil)
	if !claimed {
		return false
	}
	defer addition.Close()
	return reflect.TypeOf(addition) == want
}

func unprovenVerificationAdditions(t *testing.T, doc nightlyWorkflowDocument) []string {
	t.Helper()

	rows := nightlyMatrixRowsNamingThisPackage(doc)
	var unproven []string
	for _, kind := range usagesource.VerificationAdditions.Kinds() {
		constructor, err := usagesource.VerificationAdditions.Get(kind)
		if err != nil {
			t.Fatalf("VerificationAdditions.Get(%q) error = %v", kind, err)
		}
		addition := constructor()
		if !dropsAContextComponent(addition.Context()) {
			continue
		}
		proven := slices.ContainsFunc(rows, func(row struct {
			jobName string
			job     nightlyWorkflowJob
			row     map[string]string
		}) bool {
			return rowProvesAddition(t, row.row, reflect.TypeOf(addition))
		})
		if !proven {
			unproven = append(unproven, kind)
		}
	}
	return unproven
}

func TestEveryVerificationAdditionHasScriptedModelRow(t *testing.T) {
	t.Parallel()

	doc, err := readNightlyWorkflow()
	if err != nil {
		t.Fatalf("read the nightly workflow: %v", err)
	}

	for _, kind := range unprovenVerificationAdditions(t, doc) {
		t.Errorf("verification addition %q drops a context component and no nightly row proves it on the wire", kind)
	}
}

func droppingVerificationKinds(t *testing.T) []string {
	t.Helper()

	var kinds []string
	for _, kind := range usagesource.VerificationAdditions.Kinds() {
		constructor, err := usagesource.VerificationAdditions.Get(kind)
		if err != nil {
			t.Fatalf("VerificationAdditions.Get(%q) error = %v", kind, err)
		}
		if dropsAContextComponent(constructor().Context()) {
			kinds = append(kinds, kind)
		}
	}
	slices.Sort(kinds)
	return kinds
}

func withMatrixRows(doc nightlyWorkflowDocument, edit func(row map[string]string) bool) nightlyWorkflowDocument {
	jobs := make(map[string]nightlyWorkflowJob, len(doc.Jobs))
	for name, job := range doc.Jobs {
		var rows []map[string]string
		for _, row := range job.Strategy.Matrix.Include {
			edited := maps.Clone(row)
			if edit(edited) {
				rows = append(rows, edited)
			}
		}
		job.Strategy.Matrix.Include = rows
		jobs[name] = job
	}
	return nightlyWorkflowDocument{Jobs: jobs}
}

func TestUnprovenVerificationAdditionsNamesEveryAdditionWithoutAProvingRow(t *testing.T) {
	t.Parallel()

	shipped, err := readNightlyWorkflow()
	if err != nil {
		t.Fatalf("read the nightly workflow: %v", err)
	}
	want := droppingVerificationKinds(t)
	if len(want) == 0 {
		t.Fatal("droppingVerificationKinds() = none, want at least one addition that drops a component")
	}

	tests := []struct {
		name string
		edit func(row map[string]string) bool
	}{
		{"a document without the rows", func(map[string]string) bool { return false }},
		{"rows whose skip_filter excludes the scenario", func(row map[string]string) bool {
			row["skip_filter"] = "/" + contextScenarioSubtest
			return true
		}},
		{"rows without the model base URL variable", func(row map[string]string) bool {
			delete(row, "clientprotocol_model_base_url_env")
			delete(row, "clientprotocol_model_document_path")
			return true
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := unprovenVerificationAdditions(t, withMatrixRows(shipped, tt.edit))

			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("unprovenVerificationAdditions(%s) = %q, want %q", tt.name, got, want)
			}
		})
	}
}
