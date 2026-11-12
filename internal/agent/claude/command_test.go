package claude

import (
	"slices"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/registry"
)

func assertHasArgPair(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return
		}
	}
	t.Errorf("buildArgs() missing %q %q in [%s]", flag, value, strings.Join(args, " "))
}

func assertNoFlag(t *testing.T, args []string, flag string) {
	t.Helper()
	if slices.Contains(args, flag) {
		t.Errorf("buildArgs() unexpectedly contains flag %q in [%s]", flag, strings.Join(args, " "))
		return
	}
}

func newFirstTurnState(mcpConfigPath string) *sessionState {
	return &sessionState{
		claudeSessionID: "test-session-id",
		isContinuation:  false,
		mcpConfigPath:   mcpConfigPath,
	}
}

func TestParsePassthroughConfig_TypeFault(t *testing.T) {
	t.Parallel()

	keys := []string{
		"permission_mode",
		"model",
		"fallback_model",
		"effort",
		"allowed_tools",
		"disallowed_tools",
		"system_prompt",
		"mcp_config",
	}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			pt, fault := parsePassthroughConfig(map[string]any{key: 123})

			if fault == nil {
				t.Fatalf("parsePassthroughConfig(%s=123) fault = nil, want non-nil", key)
			}
			if fault.Key != key {
				t.Errorf("parsePassthroughConfig(%s=123) fault.Key = %q, want %q", key, fault.Key, key)
			}
			wantErr := key + ": expected string, got integer"
			if fault.Error() != wantErr {
				t.Errorf("parsePassthroughConfig(%s=123) fault.Error() = %q, want %q", key, fault.Error(), wantErr)
			}
			if pt != (passthroughConfig{}) {
				t.Errorf("parsePassthroughConfig(%s=123) passthroughConfig = %+v, want zero value", key, pt)
			}
		})
	}
}

func TestMCPInjectionConformance(t *testing.T) {
	t.Parallel()

	declared, ok := registry.Agents.Meta("claude-code")
	if !ok {
		t.Fatal(`registry.Agents.Meta("claude-code") reported not registered`)
	}
	workspace := t.TempDir()
	params := domain.StartSessionParams{
		WorkspacePath: workspace,
		AgentConfig:   domain.AgentConfig{Command: agenttest.FakeRuntime(t, t.TempDir(), "fake-claude", agenttest.OutputScenario, agenttest.Output{})},
		MCPConfigPath: agenttest.WriteSeededMCPConfig(t, workspace),
	}
	adapter, _ := NewClaudeCodeAdapter()
	session, err := adapter.StartSession(t.Context(), params)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	t.Cleanup(func() { _ = adapter.StopSession(t.Context(), session) })
	state := session.Internal.(*sessionState)
	tools, agentErr := agentcore.ResolveToolDelivery(t.Context(), params, agentcore.LaunchTarget{}, declared.MCPDialect, nil)
	if agentErr != nil {
		t.Fatalf("ResolveToolDelivery() error = %v", agentErr)
	}

	args := buildArgs(state, 1, "do work", state.passthrough)

	agenttest.AssertMCPInjection(t, declared.MCPDialect, params.MCPConfigPath, agenttest.MCPLaunchSurface{Args: args, Env: tools.LocalEnv(), File: state.mcpConfigPath})
}

func TestStartSession_MalformedMCPConfigRejected(t *testing.T) {
	t.Parallel()

	command := agenttest.FakeRuntime(t, t.TempDir(), "fake-claude", agenttest.OutputScenario, agenttest.Output{})
	adapter, _ := NewClaudeCodeAdapter()

	agenttest.AssertMalformedMCPConfigRejected(t, func(mcpConfigPath string) error {
		_, err := adapter.StartSession(t.Context(), domain.StartSessionParams{
			WorkspacePath: t.TempDir(),
			AgentConfig:   domain.AgentConfig{Command: command},
			MCPConfigPath: mcpConfigPath,
		})
		return err
	})
}

func TestBuildArgs_MCPConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mcpConfigPath string
		ptMCPConfig   string
		want          string
	}{
		{name: "worker path takes priority over operator", mcpConfigPath: "/ws/.sortie/mcp.json", ptMCPConfig: "/op/mcp.json", want: "/ws/.sortie/mcp.json"},
		{name: "operator config used when worker path absent", ptMCPConfig: "/op/mcp.json", want: "/op/mcp.json"},
		{name: "neither set produces no mcp config flag"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			args := buildArgs(newFirstTurnState(tt.mcpConfigPath), 1, "do work", passthroughConfig{MCPConfig: tt.ptMCPConfig, SessionPersistence: true})

			if tt.want == "" {
				assertNoFlag(t, args, "--mcp-config")
				return
			}
			assertHasArgPair(t, args, "--mcp-config", tt.want)
		})
	}
}

func TestBuildArgs_CredentialVerificationSwitches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                            string
		verification, full, wantReduced bool
	}{
		{name: "working launch"},
		{name: "verification launch", verification: true, wantReduced: true},
		{name: "verification launch with full context", verification: true, full: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			command := agenttest.FakeRuntime(t, t.TempDir(), "fake-claude", agenttest.OutputScenario, agenttest.Output{})
			adapter, _ := NewClaudeCodeAdapter()
			session, err := adapter.StartSession(t.Context(), domain.StartSessionParams{
				WorkspacePath:           t.TempDir(),
				AgentConfig:             domain.AgentConfig{Command: command},
				CredentialVerification:  tt.verification,
				VerificationFullContext: tt.full,
				Settings:                map[string]any{"system_prompt": "operator-sentinel", "mcp_config": "/op/mcp.json"},
			})
			if err != nil {
				t.Fatalf("StartSession() error = %v", err)
			}
			state := session.Internal.(*sessionState)

			args := buildArgs(state, 1, "Say exactly: SORTIE_CREDENTIAL_OK", state.passthrough)

			if tt.verification {
				assertHasArgPair(t, args, "--tools", "")
				assertHasFlag(t, args, "--strict-mcp-config")
				assertHasFlag(t, args, "--no-session-persistence")
				assertNoFlag(t, args, "--mcp-config")
			}
			if !tt.wantReduced {
				assertNoFlag(t, args, "--safe-mode")
				assertNoFlag(t, args, "--system-prompt")
				assertHasArgPair(t, args, "--append-system-prompt", "operator-sentinel")
				return
			}
			assertNoFlag(t, args, "--append-system-prompt")
			assertHasArgPair(t, args, "--system-prompt", agentcore.VerificationSystemPrompt)
			if n, m := countArg(args, "--safe-mode"), countArg(args, "--system-prompt"); n != 1 || m != 1 {
				t.Errorf("buildArgs() carries --safe-mode %d and --system-prompt %d times, want 1 and 1 in [%s]", n, m, strings.Join(args, " "))
			}
		})
	}
}

func countArg(args []string, arg string) (n int) {
	for _, a := range args {
		if a == arg {
			n++
		}
	}
	return n
}

func assertHasFlag(t *testing.T, args []string, flag string) {
	t.Helper()
	if !slices.Contains(args, flag) {
		t.Errorf("buildArgs() missing flag %q in [%s]", flag, strings.Join(args, " "))
	}
}
