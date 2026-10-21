package pi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/domain"
)

func TestParseRuntimeVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		out       string
		truncated bool
		want      piVersion
		wantOK    bool
	}{
		{name: "bare version", out: "1.3.0\n", want: piVersion{1, 3, 0}, wantOK: true},
		{name: "v-prefixed", out: "v0.99.2\n", want: piVersion{0, 99, 2}, wantOK: true},
		{name: "named binary", out: "pi 0.99.0\n", want: piVersion{0, 99, 0}, wantOK: true},
		{name: "prerelease suffix", out: "1.4.0-beta.1\n", want: piVersion{1, 4, 0}, wantOK: true},
		{name: "leading blank lines are skipped", out: "\n\n1.3.0\n", want: piVersion{1, 3, 0}, wantOK: true},
		{name: "empty output is not ok", out: "", wantOK: false},
		{name: "usage text carries no version", out: "Usage: pi [options]\n", wantOK: false},
		{name: "two components are not a version", out: "1.3\n", wantOK: false},
		{name: "truncated output is never ok", out: "1.3.0\n", truncated: true, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parseRuntimeVersion([]byte(tt.out), tt.truncated)
			if ok != tt.wantOK {
				t.Fatalf("parseRuntimeVersion(%q, truncated=%v) ok = %v, want %v", tt.out, tt.truncated, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("parseRuntimeVersion(%q) = %+v, want %+v", tt.out, got, tt.want)
			}
		})
	}
}

func TestPiVersionBefore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version piVersion
		want    bool
	}{
		{piVersion{0, 85, 1}, true},
		{piVersion{0, 98, 9}, true},
		{piVersion{0, 99, 0}, false},
		{piVersion{0, 99, 2}, false},
		{piVersion{0, 100, 0}, false},
		{piVersion{1, 0, 0}, false},
		{piVersion{2, 0, 18}, false},
	}

	for _, tt := range tests {
		if got := tt.version.before(minimumVersion); got != tt.want {
			t.Errorf("%v.before(%v) = %v, want %v", tt.version, minimumVersion, got, tt.want)
		}
	}
}

func TestStartSession_VersionGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		reportedVersion string
		refusedVersion  string
	}{
		{name: "0.85.1 predates --extension MCP registration", reportedVersion: "0.85.1", refusedVersion: "0.85.1"},
		{name: "0.98.9 is the last release before the minimum", reportedVersion: "0.98.9", refusedVersion: "0.98.9"},
		{name: "the minimum starts", reportedVersion: "0.99.0"},
		{name: "1.3.0 starts", reportedVersion: "1.3.0"},
		{name: "a later major starts", reportedVersion: "2.0.18"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			command := agenttest.FakeRuntime(t, dir, "pi", agenttest.OutputScenario, agenttest.Output{Version: tt.reportedVersion})
			a, err := NewPiAdapter()
			if err != nil {
				t.Fatalf("NewPiAdapter() error = %v", err)
			}

			session, err := a.StartSession(context.Background(), domain.StartSessionParams{
				WorkspacePath: dir,
				AgentConfig:   domain.AgentConfig{Command: command},
			})

			if tt.refusedVersion == "" {
				if err != nil {
					t.Fatalf("StartSession(version %q) error = %v, want nil", tt.reportedVersion, err)
				}
				if session.Internal == nil {
					t.Errorf("StartSession(version %q) session = %+v, want a started session", tt.reportedVersion, session)
				}
				return
			}
			agentErr, ok := errors.AsType[*domain.AgentError](err)
			if !ok {
				t.Fatalf("StartSession(version %q) error = %v (%T), want a *domain.AgentError", tt.reportedVersion, err, err)
			}
			if agentErr.Kind != domain.ErrAgentNotFound {
				t.Errorf("Kind = %q, want %q", agentErr.Kind, domain.ErrAgentNotFound)
			}
			wantMessage := fmt.Sprintf("pi %s is not supported; install pi 0.99.0 or later, published on npm as @earendil-works/pi-coding-agent", tt.refusedVersion)
			if agentErr.Message != wantMessage {
				t.Errorf("Message = %q, want %q", agentErr.Message, wantMessage)
			}
			if session.Internal != nil {
				t.Errorf("session.Internal = %v, want no session", session.Internal)
			}
		})
	}
}

func TestStartSession_UnreadableVersionIsRefused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	command := agenttest.FakeRuntime(t, dir, "pi", agenttest.OutputScenario, agenttest.Output{Stdout: "Usage: pi [options]\n"})
	a, _ := NewPiAdapter()

	_, err := a.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath: dir,
		AgentConfig:   domain.AgentConfig{Command: command},
	})

	agentErr, ok := errors.AsType[*domain.AgentError](err)
	if !ok || agentErr.Kind != domain.ErrAgentNotFound {
		t.Fatalf("StartSession() error = %v, want an agent_not_found *domain.AgentError", err)
	}
	const want = "the configured pi command reported no version Sortie can read: Usage: pi [options]"
	if agentErr.Message != want {
		t.Errorf("Message = %q, want %q", agentErr.Message, want)
	}
}

// TestStartSession_RefusedVersionWritesNoBridge asserts the version check
// runs before the workspace is touched.
func TestStartSession_RefusedVersionWritesNoBridge(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mcpPath := filepath.Join(dir, "mcp.json")
	const config = `{"mcpServers":{"sortie-tools":{"type":"stdio","command":"/usr/local/bin/sortie","args":["mcp-server"]}}}`
	if err := os.WriteFile(mcpPath, []byte(config), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	command := agenttest.FakeRuntime(t, dir, "pi", agenttest.OutputScenario, agenttest.Output{Version: "0.85.1"})
	a, _ := NewPiAdapter()

	_, err := a.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath: dir,
		AgentConfig:   domain.AgentConfig{Command: command},
		MCPConfigPath: mcpPath,
	})
	if err == nil {
		t.Fatal("StartSession() error = nil, want the version refusal")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".sortie", bridgeFileName)); statErr == nil {
		t.Error("bridge file exists, want none written for a refused session")
	}
}

func TestVersionQueryEnv_DropsTheServerList(t *testing.T) {
	t.Parallel()

	got := versionQueryEnv([]string{"PATH=/usr/bin", mcpServersEnv + "=stale", "HOME=/h"})
	if strings.Join(got, "|") != "PATH=/usr/bin|HOME=/h" {
		t.Errorf("versionQueryEnv() = %v, want the inherited server list dropped", got)
	}
}
