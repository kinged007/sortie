package pi

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/registry"
)

// TestValidateConfig_AcceptsEveryKey covers each accepted key on its own,
// so a key added to knownConfigKeys without a reader wired to it fails
// here rather than at runtime. Completeness runs in both directions: a
// known key with no value below, and a value below that is not a known
// key.
func TestValidateConfig_AcceptsEveryKey(t *testing.T) {
	t.Parallel()

	accepted := map[string]any{
		"model":         "anthropic/claude-sonnet-4-5",
		"thinking":      "high",
		"project_trust": "approve",
		"allowed_tools": []any{"read", "grep"},
		"denied_tools":  []any{"bash"},
	}

	for _, key := range slices.Sorted(maps.Keys(knownConfigKeys)) {
		if _, ok := accepted[key]; !ok {
			t.Errorf("knownConfigKeys has %q, want this table to carry an accepted value for it", key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(accepted)) {
		if _, ok := knownConfigKeys[key]; !ok {
			t.Errorf("this table accepts %q, want it to be a knownConfigKeys entry", key)
		}
	}

	extra := map[string]any{
		"thinking off":             map[string]any{"thinking": ""},
		"thinking max":             map[string]any{"thinking": "max"},
		"trust unset":              map[string]any{"project_trust": ""},
		"trust ignore":             map[string]any{"project_trust": "ignore"},
		"model unset":              map[string]any{"model": ""},
		"native string list":       map[string]any{"allowed_tools": []string{"read"}},
		"empty list":               map[string]any{"denied_tools": []any{}},
		"nil value reads as unset": map[string]any{"model": nil, "allowed_tools": nil},
	}

	for _, name := range slices.Sorted(maps.Keys(accepted)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertNoDiagnostics(t, map[string]any{name: accepted[name]})
		})
	}
	for _, name := range slices.Sorted(maps.Keys(extra)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertNoDiagnostics(t, extra[name].(map[string]any))
		})
	}
}

// TestValidateConfig_Refusals asserts the diagnostic key and the exact
// text of every refusal the pi block makes, so an operator's remedy
// cannot change wording without a failure here.
func TestValidateConfig_Refusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    map[string]any
		wantCheck string
		wantMsg   string
	}{
		{
			name:      "unknown key",
			config:    map[string]any{"auto_compact": false},
			wantCheck: "pi.auto_compact.unknown_key",
			wantMsg:   `unknown pi config key "auto_compact"; the pi block accepts allowed_tools, denied_tools, model, project_trust, thinking`,
		},
		{
			name:      "model wrong type",
			config:    map[string]any{"model": 123},
			wantCheck: "pi.model.wrong_type",
			wantMsg:   "model: expected string, got integer",
		},
		{
			name:      "thinking wrong type",
			config:    map[string]any{"thinking": true},
			wantCheck: "pi.thinking.wrong_type",
			wantMsg:   "thinking: expected string, got boolean",
		},
		{
			name:      "project_trust wrong type",
			config:    map[string]any{"project_trust": []any{"approve"}},
			wantCheck: "pi.project_trust.wrong_type",
			wantMsg:   "project_trust: expected string, got list",
		},
		{
			name:      "thinking invalid value",
			config:    map[string]any{"thinking": "turbo"},
			wantCheck: "pi.thinking.invalid_value",
			wantMsg:   `thinking: "turbo" is not a pi thinking level; valid values are off, minimal, low, medium, high, xhigh, max`,
		},
		{
			name:      "project_trust invalid value",
			config:    map[string]any{"project_trust": "maybe"},
			wantCheck: "pi.project_trust.invalid_value",
			wantMsg:   `project_trust: "maybe" is not a pi trust setting; valid values are ignore, approve`,
		},
		{
			name:      "allowed_tools is not a list",
			config:    map[string]any{"allowed_tools": "read"},
			wantCheck: "pi.allowed_tools.wrong_type",
			wantMsg:   "allowed_tools: expected list, got string",
		},
		{
			name:      "denied_tools is not a list",
			config:    map[string]any{"denied_tools": 5},
			wantCheck: "pi.denied_tools.wrong_type",
			wantMsg:   "denied_tools: expected list, got integer",
		},
		{
			name:      "allowed_tools element is not a name",
			config:    map[string]any{"allowed_tools": []any{"read", 4}},
			wantCheck: "pi.allowed_tools.malformed_list",
			wantMsg:   "allowed_tools[1]: expected a tool name, got integer",
		},
		{
			name:      "denied_tools element is blank",
			config:    map[string]any{"denied_tools": []any{" "}},
			wantCheck: "pi.denied_tools.malformed_list",
			wantMsg:   "denied_tools[0]: tool name is empty",
		},
		{
			name:      "overlap names every conflicting tool in order",
			config:    map[string]any{"allowed_tools": []any{"write", "bash", "read"}, "denied_tools": []any{"bash", "write"}},
			wantCheck: "pi.allowed_tools.overlap",
			wantMsg:   "allowed_tools and denied_tools overlap: bash, write",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			diags := validateConfig(registry.AgentConfigFields{Kind: "pi", Passthrough: tt.config})
			if len(diags) != 1 {
				t.Fatalf("validateConfig() = %+v, want exactly one diagnostic for %v", diags, tt.config)
			}
			if diags[0].Severity != "error" {
				t.Errorf("Severity = %q, want %q", diags[0].Severity, "error")
			}
			if diags[0].Check != tt.wantCheck {
				t.Errorf("Check = %q, want %q", diags[0].Check, tt.wantCheck)
			}
			if diags[0].Message != tt.wantMsg {
				t.Errorf("Message = %q, want %q", diags[0].Message, tt.wantMsg)
			}
		})
	}
}

// TestValidateConfig_ReportsEveryFaultOnce pins the two properties an
// operator fixing a pi block depends on: every mistake in one pass, and
// one diagnostic per mistake, so a list with two bad elements does not
// read as one fault.
func TestValidateConfig_ReportsEveryFaultOnce(t *testing.T) {
	t.Parallel()

	diags := validateConfig(registry.AgentConfigFields{Kind: "pi", Passthrough: map[string]any{
		"model":         123,
		"thinking":      "turbo",
		"project_trust": "maybe",
		"denied_tools":  []any{4, " "},
		"allowed_tools": "read",
		"auto_compact":  false,
	}})

	var checks []string
	for _, d := range diags {
		checks = append(checks, d.Check)
		if d.Severity != "error" {
			t.Errorf("severity = %q, want %q", d.Severity, "error")
		}
	}

	want := []string{
		"pi.auto_compact.unknown_key",
		"pi.model.wrong_type",
		"pi.thinking.invalid_value",
		"pi.project_trust.invalid_value",
		"pi.allowed_tools.wrong_type",
		"pi.denied_tools.malformed_list",
		"pi.denied_tools.malformed_list",
	}
	if strings.Join(checks, "|") != strings.Join(want, "|") {
		t.Errorf("checks = %v, want %v", checks, want)
	}
	for _, message := range []string{"denied_tools[0]: expected a tool name, got integer", "denied_tools[1]: tool name is empty"} {
		var found bool
		for _, d := range diags {
			if d.Message == message {
				found = true
			}
		}
		if !found {
			t.Errorf("no diagnostic carries %q", message)
		}
	}
}

// assertNoDiagnostics fails t when validateConfig reports anything for
// config.
func assertNoDiagnostics(t *testing.T, config map[string]any) {
	t.Helper()

	diags := validateConfig(registry.AgentConfigFields{Kind: "pi", Passthrough: config})
	if len(diags) != 0 {
		t.Errorf("validateConfig(%v) = %+v, want no diagnostics", config, diags)
	}
}
