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

			pt, faults := parsePassthroughConfig(tt.config)
			hasErr := len(faults) > 0

			if tt.wantErr {
				if !hasErr {
					t.Fatal("parsePassthroughConfig() faults = nil, want error")
				}
				joined := ""
				for _, f := range faults {
					joined += f.Message + "; "
				}
				if !strings.Contains(joined, "bash") {
					t.Errorf("faults = %q, want them to mention %q", joined, "bash")
				}
				return
			}

			if hasErr {
				t.Fatalf("parsePassthroughConfig() faults = %v", faults)
			}

			if tt.checkFunc != nil {
				tt.checkFunc(t, pt)
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

			_, faults := parsePassthroughConfig(tt.config)
			if len(faults) == 0 {
				t.Fatal("parsePassthroughConfig() faults = nil, want a refusal")
			}
			joined := ""
			for _, f := range faults {
				joined += f.Message + "; "
			}
			if !strings.Contains(joined, tt.wantInMsg) {
				t.Errorf("faults = %q, want them to contain %q", joined, tt.wantInMsg)
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
			if !strings.Contains(joined, found.Message) {
				t.Errorf("validateConfig() message = %q, want parse faults %q to contain it", found.Message, joined)
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

	if _, faults := parsePassthroughConfig(config); len(faults) != 0 {
		t.Errorf("parsePassthroughConfig() faults = %+v, want none: the agent block's own keys are not pi block keys", faults)
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
// environment the pi process actually received, and asserts the
// translated servers reach it locally but not over SSH. Capturing the
// launch rather than composing one is what makes both assertions able
// to fail.
func TestMCPInjectionConformance(t *testing.T) {
	t.Parallel()

	declared, ok := registry.Agents.Meta("pi")
	if !ok {
		t.Fatal(`registry.Agents.Meta("pi") reported not registered`)
	}
	if declared.MCPInjection != registry.MCPInjectionTranslated {
		t.Fatalf("registered MCPInjection = %q, want %q", declared.MCPInjection, registry.MCPInjectionTranslated)
	}
	if !declared.MCPInjection.DeliversTools(false) {
		t.Error("MCPInjection.DeliversTools(remote=false) = false, want true: a local pi session must reach Sortie tools")
	}
	if declared.MCPInjection.DeliversTools(true) {
		t.Error("MCPInjection.DeliversTools(remote=true) = true, want false: a remote pi launch cannot deliver Sortie tools")
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

	// Local launch: the bridge extension and the env JSON both carry the
	// server detail, which the Translated assertion looks for as
	// substrings on the launch surface.
	t.Run("local launch delivers the translated servers", func(t *testing.T) {
		t.Parallel()

		localDir := filepath.Join(dir, "local")
		if err := os.MkdirAll(filepath.Join(localDir, ".sortie"), 0o750); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(localDir, ".sortie", "mcp.json"), []byte(generatedConfig), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		localMCP := filepath.Join(localDir, ".sortie", "mcp.json")
		argvFile := filepath.Join(localDir, "argv.txt")
		envFile := filepath.Join(localDir, "env.txt")
		script := writePiScript(t, localDir, `printf '%s\n' "$*" > `+argvFile+`
env > `+envFile+`
exit 0`)

		a, err := NewPiAdapter()
		if err != nil {
			t.Fatalf("NewPiAdapter() error = %v", err)
		}
		session, err := a.StartSession(context.Background(), domain.StartSessionParams{
			WorkspacePath:   localDir,
			AgentConfig:     domain.AgentConfig{Command: script},
			MCPConfigPath:   localMCP,
			ResumeSessionID: "",
		})
		if err != nil {
			t.Fatalf("StartSession() error = %v", err)
		}
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
		agenttest.AssertMCPInjection(t, declared.MCPInjection, localMCP, surface)
	})

	// Remote launch: no Sortie server detail must reach the launch.
	t.Run("remote launch delivers no tool servers", func(t *testing.T) {
		t.Parallel()

		remoteDir := filepath.Join(dir, "remote")
		if err := os.MkdirAll(filepath.Join(remoteDir, ".sortie"), 0o750); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(remoteDir, ".sortie", "mcp.json"), []byte(generatedConfig), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		// The remote's server-side launch is not observable from this
		// process, but the translation itself must already be nil so no
		// bridge file or env value is built for it. Rendering the arg
		// slice from the translated result is enough to drive the
		// Unsupported assertion against its launch surface.
		servers, err := translateMCPServers(mcpConfigPath, true)
		if err != nil {
			t.Fatalf("translateMCPServers() error = %v", err)
		}
		if servers != nil {
			t.Fatalf("translateMCPServers(remote=true) = %v, want nil", servers)
		}
		doc, err := buildMCPEnvValue(servers)
		if err != nil {
			t.Fatalf("buildMCPEnvValue() error = %v", err)
		}
		if doc != "" {
			t.Fatalf("buildMCPEnvValue(remote) = %q, want %q", doc, "")
		}
		state := newTestSessionState(remoteDir, "")
		args := buildRunArgs(state, "work", passthroughConfig{ProjectTrust: trustApprove})
		env := []string{"PATH=/usr/bin"}
		if doc != "" {
			env = append(env, "SORTIE_PI_MCP_SERVERS="+doc)
		}
		agenttest.AssertMCPInjection(t, registry.MCPInjectionUnsupported, mcpConfigPath, agenttest.MCPLaunchSurface{Args: args, Env: env})
	})
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
	return sshutil.BuildSSHLaunch(target.SSHHost, target.WorkspacePath, target.RemoteCommand, cmdArgs, sshutil.SSHOptions{}).Args
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
