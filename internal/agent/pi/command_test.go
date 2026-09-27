package pi

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/agent/sshutil"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/registry"
)

// assertHasArgPair fails if flag and value do not appear as consecutive
// elements in args.
func assertHasArgPair(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i := range len(args) - 1 {
		if args[i] == flag && args[i+1] == value {
			return
		}
	}
	t.Errorf("buildRunArgs() missing %q %q in [%s]", flag, value, strings.Join(args, " "))
}

// assertHasFlag fails if flag does not appear in args.
func assertHasFlag(t *testing.T, args []string, flag string) {
	t.Helper()
	if slices.Contains(args, flag) {
		return
	}
	t.Errorf("buildRunArgs() missing flag %q in [%s]", flag, strings.Join(args, " "))
}

// assertNoFlag fails if flag appears anywhere in args.
func assertNoFlag(t *testing.T, args []string, flag string) {
	t.Helper()
	if slices.Contains(args, flag) {
		t.Errorf("buildRunArgs() unexpected flag %q in [%s]", flag, strings.Join(args, " "))
	}
}

// newTestSessionState returns a sessionState suitable for buildRunArgs tests.
func newTestSessionState(workspacePath, sessionID string) *sessionState {
	return &sessionState{
		target: agentcore.LaunchTarget{
			WorkspacePath: workspacePath,
		},
		sessionID: sessionID,
	}
}

func TestNewPiAdapter_ParsePassthroughConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    map[string]any
		wantErr   bool
		checkFunc func(t *testing.T, pt passthroughConfig)
	}{
		{
			name:   "defaults",
			config: map[string]any{},
			checkFunc: func(t *testing.T, pt passthroughConfig) {
				t.Helper()
				want := passthroughConfig{ProjectTrust: trustIgnore}
				if !reflect.DeepEqual(pt, want) {
					t.Errorf("passthroughConfig = %+v, want %+v (an unattended turn defaults to --no-approve)", pt, want)
				}
			},
		},
		{
			name: "allowed_tools_parse",
			config: map[string]any{
				"allowed_tools": []any{"read", "edit"},
			},
			checkFunc: func(t *testing.T, pt passthroughConfig) {
				t.Helper()
				if !slices.Equal(pt.AllowedTools, []string{"read", "edit"}) {
					t.Errorf("AllowedTools = %v, want [read edit]", pt.AllowedTools)
				}
			},
		},
		{
			name: "denied_tools_parse",
			config: map[string]any{
				"denied_tools": []any{"bash"},
			},
			checkFunc: func(t *testing.T, pt passthroughConfig) {
				t.Helper()
				if !slices.Equal(pt.DeniedTools, []string{"bash"}) {
					t.Errorf("DeniedTools = %v, want [bash]", pt.DeniedTools)
				}
			},
		},
		{
			name: "unknown_extension_tool_names_preserved",
			config: map[string]any{
				"allowed_tools": []any{"customtool"},
			},
			checkFunc: func(t *testing.T, pt passthroughConfig) {
				t.Helper()
				if !slices.Equal(pt.AllowedTools, []string{"customtool"}) {
					t.Errorf("AllowedTools = %v, want [customtool]", pt.AllowedTools)
				}
			},
		},
		{
			name: "overlap_error",
			config: map[string]any{
				"allowed_tools": []any{"bash"},
				"denied_tools":  []any{"bash"},
			},
			wantErr: true,
		},
		{
			name: "model_and_flags",
			config: map[string]any{
				"model":         "anthropic/claude-3-5-sonnet",
				"thinking":      "xhigh",
				"project_trust": "approve",
			},
			checkFunc: func(t *testing.T, pt passthroughConfig) {
				t.Helper()
				want := passthroughConfig{
					Model:        "anthropic/claude-3-5-sonnet",
					Thinking:     "xhigh",
					ProjectTrust: trustApprove,
				}
				if !reflect.DeepEqual(pt, want) {
					t.Errorf("passthroughConfig = %+v, want %+v", pt, want)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, err := NewPiAdapter(tt.config)

			if tt.wantErr {
				if err == nil {
					t.Fatal("NewPiAdapter() error = nil, want error")
				}
				if !strings.Contains(err.Error(), "bash") {
					t.Errorf("error = %q, want it to mention %q", err.Error(), "bash")
				}
				return
			}

			if err != nil {
				t.Fatalf("NewPiAdapter() error = %v", err)
			}

			oc, ok := a.(*PiAdapter)
			if !ok {
				t.Fatalf("adapter type = %T, want *PiAdapter", a)
			}
			if tt.checkFunc != nil {
				tt.checkFunc(t, oc.passthrough)
			}
		})
	}
}

// TestParsePassthroughConfig_Faults covers every refusal the funnel
// makes, so a new key whose fault arm was never wired shows up here
// rather than at runtime.
func TestParsePassthroughConfig_Faults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    map[string]any
		wantCheck string
		wantInMsg string
	}{
		{"model wrong type", map[string]any{"model": 123}, "pi.model.wrong_type", "model: expected string, got integer"},
		{"thinking wrong type", map[string]any{"thinking": true}, "pi.thinking.wrong_type", "thinking: expected string, got boolean"},
		{"trust wrong type", map[string]any{"project_trust": 7}, "pi.project_trust.wrong_type", "project_trust: expected string, got integer"},
		{"thinking invalid value", map[string]any{"thinking": "turbo"}, "pi.thinking.invalid_value", `"turbo" is not a pi thinking level`},
		{"trust invalid value", map[string]any{"project_trust": "maybe"}, "pi.project_trust.invalid_value", `"maybe" is not a pi trust setting`},
		{"allowed_tools wrong type", map[string]any{"allowed_tools": "read"}, "pi.allowed_tools.wrong_type", "allowed_tools: expected list, got string"},
		{"denied_tools wrong type", map[string]any{"denied_tools": 5}, "pi.denied_tools.wrong_type", "denied_tools: expected list, got integer"},
		{"allowed_tools non-string element", map[string]any{"allowed_tools": []any{"read", 4}}, "pi.allowed_tools.malformed_list", "allowed_tools[1]: expected a tool name, got integer"},
		{"denied_tools empty name", map[string]any{"denied_tools": []any{" "}}, "pi.denied_tools.malformed_list", "denied_tools[0]: tool name is empty"},
		{"unknown key", map[string]any{"auto_compact": false}, "pi.auto_compact.unknown_key", `unknown pi config key "auto_compact"`},
		{"overlap", map[string]any{"allowed_tools": []any{"read", "bash"}, "denied_tools": []any{"bash"}}, "pi.allowed_tools.overlap", "overlap: bash"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewPiAdapter(tt.config)
			if err == nil {
				t.Fatal("NewPiAdapter() error = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.wantInMsg) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantInMsg)
			}

			diags := validateConfig(registry.AgentConfigFields{Kind: "pi", Passthrough: tt.config})
			var found *registry.ValidationDiag
			for i := range diags {
				if diags[i].Check == tt.wantCheck {
					found = &diags[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("validateConfig() diags = %+v, want one with check %q", diags, tt.wantCheck)
			}
			if found.Message != err.Error() {
				t.Errorf("validateConfig() message = %q, want it byte-identical to the constructor's %q", found.Message, err.Error())
			}
		})
	}
}

// TestParsePassthroughConfig_GenericAgentKeysAreNotPiKeys pins the
// boundary between the two sources of the config map. The agent block
// contributes kind, command, and the four timeout keys to every
// adapter's map, so a pi validator that read them as pi block keys
// would refuse every valid workflow rather than a misspelled option.
func TestParsePassthroughConfig_GenericAgentKeysAreNotPiKeys(t *testing.T) {
	t.Parallel()

	config := map[string]any{
		"kind":             "pi",
		"command":          "pi",
		"turn_timeout_ms":  3_600_000,
		"read_timeout_ms":  5_000,
		"stall_timeout_ms": 300_000,
		"stop_grace_ms":    5_000,
		"project_trust":    "ignore",
	}

	if _, err := NewPiAdapter(config); err != nil {
		t.Errorf("NewPiAdapter() error = %v, want nil: the agent block's own keys are not pi block keys", err)
	}
	if diags := validateConfig(registry.AgentConfigFields{Kind: "pi", Passthrough: config}); len(diags) != 0 {
		t.Errorf("validateConfig() diags = %+v, want none", diags)
	}
}

func TestParsePassthroughConfig_ReportsEveryFault(t *testing.T) {
	t.Parallel()

	config := map[string]any{
		"model":         123,
		"thinking":      "turbo",
		"project_trust": "maybe",
		"denied_tools":  "bash",
		"auto_compact":  false,
	}

	diags := validateConfig(registry.AgentConfigFields{Kind: "pi", Passthrough: config})
	if len(diags) != 5 {
		t.Fatalf("validateConfig() reported %d diagnostics, want 5: %+v", len(diags), diags)
	}
	for _, d := range diags {
		if d.Severity != "error" {
			t.Errorf("severity = %q, want %q", d.Severity, "error")
		}
	}
}

func TestBuildRunArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		sessionID   string
		pt          passthroughConfig
		prompt      string
		wantPresent []string
		wantPairs   [][2]string
		wantAbsent  []string
	}{
		{
			name:       "fresh_session",
			sessionID:  "",
			pt:         passthroughConfig{},
			prompt:     "do work",
			wantAbsent: []string{"--session"},
		},
		{
			name:      "resume_session",
			sessionID: "ses_abc",
			pt:        passthroughConfig{},
			prompt:    "continue",
			wantPairs: [][2]string{{"--session", "ses_abc"}},
		},
		{
			name:        "noninteractive_json_mode",
			sessionID:   "",
			pt:          passthroughConfig{},
			prompt:      "work",
			wantPresent: []string{"-p", "--mode"},
			wantPairs:   [][2]string{{"--mode", "json"}},
		},
		{
			name:      "model_flag",
			sessionID: "",
			pt:        passthroughConfig{Model: "anthropic/claude-3-5-sonnet"},
			prompt:    "work",
			wantPairs: [][2]string{{"--model", "anthropic/claude-3-5-sonnet"}},
		},
		{
			name:      "thinking_flag",
			sessionID: "",
			pt:        passthroughConfig{Thinking: "medium"},
			prompt:    "work",
			wantPairs: [][2]string{{"--thinking", "medium"}},
		},
		{
			name:      "tool_lists_join_with_commas",
			sessionID: "",
			pt:        passthroughConfig{AllowedTools: []string{"read", "grep"}, DeniedTools: []string{"bash", "write"}},
			prompt:    "work",
			wantPairs: [][2]string{{"--tools", "read,grep"}, {"--exclude-tools", "bash,write"}},
		},
		{
			name:      "trust_approve",
			sessionID: "",
			pt:        passthroughConfig{ProjectTrust: trustApprove},
			prompt:    "work",
			wantPairs: [][2]string{{"--approve", ""}},
		},
		{
			name:      "prompt_after_dashdash",
			sessionID: "",
			pt:        passthroughConfig{},
			prompt:    "my --prompt with flags",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state := newTestSessionState("/tmp/workspace", tt.sessionID)
			args := buildRunArgs(state, tt.prompt, tt.pt)

			for _, flag := range tt.wantPresent {
				assertHasFlag(t, args, flag)
			}
			for _, pair := range tt.wantPairs {
				if pair[1] == "" {
					assertHasFlag(t, args, pair[0])
					continue
				}
				assertHasArgPair(t, args, pair[0], pair[1])
			}
			for _, flag := range tt.wantAbsent {
				assertNoFlag(t, args, flag)
			}

			// An unattended turn is launched with the trust decision
			// stated explicitly, never left to a prompt no one answers.
			if tt.pt.ProjectTrust != trustApprove {
				if !slices.Contains(args, "--no-approve") {
					t.Errorf("args = %v, want --no-approve unless project_trust is approve", args)
				}
				assertNoFlag(t, args, "--approve")
			} else {
				assertNoFlag(t, args, "--no-approve")
			}

			// Prompt must be the last argument, after "--".
			if len(args) < 2 {
				t.Fatalf("args too short: %v", args)
			}
			lastTwo := args[len(args)-2:]
			if lastTwo[0] != "--" {
				t.Errorf("second-to-last arg = %q, want %q", lastTwo[0], "--")
			}
			if lastTwo[1] != tt.prompt {
				t.Errorf("last arg = %q, want prompt %q", lastTwo[1], tt.prompt)
			}
		})
	}
}

// TestBuildRunArgs_NoOpencodeFlagsRemains asserts that no environment
// key from a different CLI is still being delivered: pi takes every
// restriction it honors as a flag, so an OPENCODE_* variable would be
// an operator-visible setting that does nothing.
func TestBuildRunArgs_NoOpencodeFlagsRemains(t *testing.T) {
	t.Parallel()

	state := newTestSessionState("/tmp/workspace", "")
	args := buildRunArgs(state, "work", passthroughConfig{
		AllowedTools: []string{"read"},
		DeniedTools:  []string{"bash"},
		Thinking:     "high",
		ProjectTrust: trustApprove,
	})

	want := []string{"-p", "--mode", "json", "--thinking", "high", "--tools", "read", "--exclude-tools", "bash", "--approve", "--", "work"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", args, want)
	}
}

// TestMCPInjectionConformance drives a real turn for a session that
// carries the worker-generated MCP config path, captures the argv and
// environment the pi process actually received, and asserts neither the
// path nor the Sortie server command reaches it. Capturing the launch
// rather than composing one is what makes this negative assertion able
// to fail: an adapter that delivered the document through any channel
// would have it in one of these two files.
func TestMCPInjectionConformance(t *testing.T) {
	t.Parallel()

	declared, ok := registry.Agents.Meta("pi")
	if !ok {
		t.Fatal(`registry.Agents.Meta("pi") reported not registered`)
	}
	if declared.MCPInjection != registry.MCPInjectionUnsupported {
		t.Fatalf("registered MCPInjection = %q, want %q", declared.MCPInjection, registry.MCPInjectionUnsupported)
	}
	// The same disposition is what the generic preflight reads to warn
	// that a pi session can reach no tool at all. A local and a remote
	// launch must both deliver none, or that warning is a false alarm
	// in one of the two modes.
	for _, remote := range []bool{false, true} {
		if declared.MCPInjection.DeliversTools(remote) {
			t.Errorf("MCPInjection.DeliversTools(remote=%t) = true, want false: a pi session can reach no Sortie tool in either launch mode", remote)
		}
	}

	dir := t.TempDir()
	mcpConfigPath := filepath.Join(dir, ".sortie", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpConfigPath), 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	const generatedConfig = `{"mcpServers":{"sortie-tools":{"type":"stdio","command":"/usr/local/bin/sortie","args":["mcp-server","--workflow","/repo/WORKFLOW.md"],"env":{"SORTIE_ISSUE_ID":"abc-123"}}}}`
	if err := os.WriteFile(mcpConfigPath, []byte(generatedConfig), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	argvFile := filepath.Join(dir, "argv.txt")
	envFile := filepath.Join(dir, "env.txt")
	script := writePiScript(t, dir, `printf '%s\n' "$*" > `+argvFile+`
env > `+envFile+`
exit 0`)

	a, err := NewPiAdapter(map[string]any{})
	if err != nil {
		t.Fatalf("NewPiAdapter() error = %v", err)
	}
	session, err := a.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath:   dir,
		AgentConfig:     domain.AgentConfig{Command: script},
		MCPConfigPath:   mcpConfigPath,
		ResumeSessionID: "",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	// The fake process writes its launch surface and then exits without a
	// JSON response, so the shared no-work rule returns an error here. The
	// launch capture is still the subject of this test.
	_, _, _ = collectEvents(t, a, session, "work")

	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("ReadFile(argv) error = %v", err)
	}
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("ReadFile(env) error = %v", err)
	}

	surface := agenttest.MCPLaunchSurface{
		Args: strings.Split(strings.TrimRight(string(argv), "\n"), "\n"),
		Env:  strings.Split(strings.TrimRight(string(env), "\n"), "\n"),
	}
	agenttest.AssertMCPInjection(t, declared.MCPInjection, mcpConfigPath, surface)
	// The generated document names the server and its command, so their
	// absence is a second, independent reading of the same capture.
	for _, forbidden := range []string{"sortie-tools", "/usr/local/bin/sortie", "mcp-server"} {
		if strings.Contains(string(argv)+string(env), forbidden) {
			t.Errorf("launch surface carries %q, want the pi process to receive no Sortie MCP detail", forbidden)
		}
	}
}

// TestRemoteLaunch_ForwardsSessionAndWorkspace asserts the SSH launch
// keeps --session on the remote command line and runs the turn in the
// workspace.
func TestRemoteLaunch_ForwardsSessionAndWorkspace(t *testing.T) {
	t.Parallel()

	state := newTestSessionState("/remote/ws", "ses_remote")
	args := buildRunArgs(state, "work", passthroughConfig{ProjectTrust: trustApprove})

	sshArgs := buildSSHArgsForTest(state, args)
	joined := strings.Join(sshArgs, " ")

	for _, want := range []string{"/remote/ws", "--session", "ses_remote", "--approve", "--", "work"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ssh args = %q, want them to carry %q", joined, want)
		}
	}
}

// buildSSHArgsForTest builds the ssh argv the adapter builds for a
// remote launch of the given run arguments.
func buildSSHArgsForTest(state *sessionState, cmdArgs []string) []string {
	target := state.target
	target.RemoteCommand = "pi"
	target.SSHHost = "host"
	return sshutil.BuildSSHArgs(target.SSHHost, target.WorkspacePath, target.RemoteCommand, cmdArgs, sshutil.SSHOptions{})
}

// TestValidateConfig_NoFaultsForSupportedConfiguration asserts a pi
// block written with every accepted key passes the offline validator.
func TestValidateConfig_NoFaultsForSupportedConfiguration(t *testing.T) {
	t.Parallel()

	diags := validateConfig(registry.AgentConfigFields{
		Kind: "pi",
		Passthrough: map[string]any{
			"model":         "anthropic/claude-3-5-sonnet",
			"thinking":      "off",
			"project_trust": "ignore",
			"allowed_tools": []any{"read", "grep"},
			"denied_tools":  []any{"bash"},
		},
	})
	if len(diags) != 0 {
		t.Errorf("validateConfig() = %+v, want no diagnostics", diags)
	}
}
