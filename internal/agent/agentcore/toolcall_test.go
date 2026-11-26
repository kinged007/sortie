package agentcore

import (
	"encoding/json"
	"testing"

	"github.com/sortie-ai/sortie/internal/domain"
)

func TestMapToolCall(t *testing.T) {
	t.Parallel()

	table := map[string]ToolMapping{
		"Bash":  {Kind: domain.ToolKindShell, Key: "command"},
		"Skill": {Kind: domain.ToolKindSkill, Key: "skill"},
	}

	tests := []struct {
		name  string
		tool  string
		input string
		want  ToolCall
	}{
		{"mapped name gives kind and key value", "Bash", `{"command":"git status","description":"x"}`, ToolCall{"Bash", domain.ToolKindShell, "git status"}},
		{"skill args are never read", "Skill", `{"skill":"demo","args":"SENTINEL"}`, ToolCall{"Skill", domain.ToolKindSkill, "demo"}},
		{"absent member", "Bash", `{"description":"x"}`, ToolCall{Name: "Bash", Kind: domain.ToolKindShell}},
		{"null member", "Bash", `{"command":null}`, ToolCall{Name: "Bash", Kind: domain.ToolKindShell}},
		{"numeric member", "Bash", `{"command":7}`, ToolCall{Name: "Bash", Kind: domain.ToolKindShell}},
		{"array input", "Bash", `["git status"]`, ToolCall{Name: "Bash", Kind: domain.ToolKindShell}},
		{"unmapped name is an other tool", "Grep", `{"command":"git status"}`, ToolCall{Name: "Grep"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := MapToolCall(table, tt.tool, json.RawMessage(tt.input))

			if got != tt.want {
				t.Errorf("MapToolCall(%q, %s) = %+v, want %+v", tt.tool, tt.input, got, tt.want)
			}
		})
	}
}

func TestQualifiedToolName(t *testing.T) {
	t.Parallel()

	if got, want := QualifiedToolName("github", "get_issue"), "github.get_issue"; got != want {
		t.Errorf("QualifiedToolName(%q, %q) = %q, want %q", "github", "get_issue", got, want)
	}
	if got, want := QualifiedToolName("", "get_issue"), "get_issue"; got != want {
		t.Errorf("QualifiedToolName(%q, %q) = %q, want %q", "", "get_issue", got, want)
	}
}
