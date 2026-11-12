package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/agent/mcpconfig"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/registry"
)

func TestParsePassthroughConfig_TypeFault(t *testing.T) {
	t.Parallel()

	keys := []string{
		"model",
		"effort",
		"approval_policy",
		"thread_sandbox",
		"personality",
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
			if pt.Model != "" || pt.Effort != "" || pt.ApprovalPolicy != "" || pt.ThreadSandbox != "" || pt.Personality != "" || pt.TurnSandboxPolicy != nil {
				t.Errorf("parsePassthroughConfig(%s=123) passthroughConfig = %+v, want zero value", key, pt)
			}
		})
	}
}

const scenarioRecordLaunch = "codex.record-launch"

func init() {
	fakeScenarios[scenarioRecordLaunch] = agenttest.Typed(func(args []string, params recordLaunchParams) int {
		launch, err := json.Marshal(recordedLaunch{Args: args, Env: os.Environ()})
		if err != nil || os.WriteFile(params.LaunchPath, launch, 0o600) != nil {
			return 1
		}
		return serveRecordFrames(newFakeAppServerScanner(), os.Stdout, recordFramesParams{RecordPath: params.FramesPath})
	})
}

type recordLaunchParams struct {
	LaunchPath string `json:"launchPath"`
	FramesPath string `json:"framesPath"`
}

type recordedLaunch struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
}

func startSessionRecordingLaunch(t *testing.T) (mcpConfigPath string, launch recordedLaunch) {
	t.Helper()

	dir := t.TempDir()
	workspace := t.TempDir()
	launchPath := filepath.Join(dir, "launch.json")
	mcpConfigPath = agenttest.WriteSeededMCPConfig(t, workspace)
	adapter := &CodexAdapter{}

	session, err := adapter.StartSession(t.Context(), domain.StartSessionParams{
		WorkspacePath: workspace,
		AgentConfig: domain.AgentConfig{
			Command: agenttest.FakeRuntime(t, dir, "codex", scenarioRecordLaunch, recordLaunchParams{LaunchPath: launchPath, FramesPath: filepath.Join(dir, "frames.jsonl")}),
		},
		MCPConfigPath: mcpConfigPath,
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := adapter.StopSession(context.Background(), session); err != nil {
			t.Errorf("StopSession() error = %v, want nil", err)
		}
	})

	raw, err := os.ReadFile(launchPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", launchPath, err)
	}
	if err := json.Unmarshal(raw, &launch); err != nil {
		t.Fatalf("Unmarshal(%q): %v", launchPath, err)
	}
	return mcpConfigPath, launch
}

func TestMCPInjectionConformance(t *testing.T) {
	t.Parallel()

	declared, ok := registry.Agents.Meta("codex")
	if !ok {
		t.Fatal(`registry.Agents.Meta("codex") reported not registered`)
	}

	mcpConfigPath, launch := startSessionRecordingLaunch(t)

	agenttest.AssertMCPInjection(t, declared.MCPDialect, mcpConfigPath, agenttest.MCPLaunchSurface{Args: launch.Args, Env: launch.Env})
}

func TestStartSession_MalformedMCPConfigRejected(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()

	agenttest.AssertMalformedMCPConfigRejected(t, func(mcpConfigPath string) error {
		_, err := (&CodexAdapter{}).StartSession(t.Context(), domain.StartSessionParams{
			WorkspacePath: workspace,
			AgentConfig:   domain.AgentConfig{Command: "sh -c"},
			MCPConfigPath: mcpConfigPath,
		})
		return err
	})
}

func TestPlaceToolServers_CarriesArgsOnly(t *testing.T) {
	t.Parallel()

	delivery := mcpconfig.Delivery{
		Args: []string{"-c", "mcp_servers.sortie-tools={url=\"http://127.0.0.1:1/mcp\"}"},
		Env:  []string{"STDIO_SECRET=seeded"},
	}

	got := placeToolServers(delivery)

	if !slices.Equal(got.Args, delivery.Args) {
		t.Errorf("placeToolServers().Args = %q, want %q", got.Args, delivery.Args)
	}
	if len(got.Env) != 0 {
		t.Errorf("placeToolServers().Env = %v, want none: a delivery env entry must not leave the orchestrator host", got.Env)
	}
}
