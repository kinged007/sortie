package codex

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/domain"
)

// capturingWriteCloser records each Write call as one entry, in the order
// received, so a test can assert on the exact bytes a handshake function
// or the RunTurn event loop wrote back to the app-server. Safe for
// concurrent use: every production caller writes through state.conn,
// which owns its own write mutex rather than state.mu.
type capturingWriteCloser struct {
	mu     sync.Mutex
	writes []string
}

func (w *capturingWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

func (w *capturingWriteCloser) Close() error { return nil }

// find returns the first captured write with the given prefix.
func (w *capturingWriteCloser) find(prefix string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, write := range w.writes {
		if strings.HasPrefix(write, prefix) {
			return write, true
		}
	}
	return "", false
}

func TestIsAgentError_WithAgentError(t *testing.T) {
	t.Parallel()

	ae := &domain.AgentError{Kind: domain.ErrPortExit, Message: "subprocess exited"}
	var target *domain.AgentError
	if !isAgentError(ae, &target) {
		t.Fatal("isAgentError() = false for *domain.AgentError")
	}
	if target != ae {
		t.Error("isAgentError() did not set target to the input error")
	}
}

func TestIsAgentError_WithPlainError(t *testing.T) {
	t.Parallel()

	plain := errors.New("not an agent error")
	var target *domain.AgentError
	if isAgentError(plain, &target) {
		t.Fatal("isAgentError() = true for plain error")
	}
	if target != nil {
		t.Error("isAgentError() set target for plain error")
	}
}

func TestStartSession_SSHBinaryNotFound(t *testing.T) {
	// No t.Parallel(): uses t.Setenv which mutates process env.
	t.Setenv("PATH", "/nonexistent-path-for-test")

	adapter, _ := NewCodexAdapter()
	_, err := adapter.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath: t.TempDir(),
		SSHHost:       "remote.example.com",
		AgentConfig:   domain.AgentConfig{Command: "codex app-server"},
	})
	requireAgentError(t, err, domain.ErrAgentNotFound)
}

func TestBuildSandboxPolicy_CredentialVerificationIsAlwaysReadOnly(t *testing.T) {
	t.Parallel()

	state := &sessionState{
		credentialVerification: true,
		target:                 agentcore.LaunchTarget{WorkspacePath: "/ws"},
	}
	pt := passthroughConfig{
		ThreadSandbox:     "dangerFullAccess",
		TurnSandboxPolicy: map[string]any{"type": "danger-full-access", "networkAccess": true},
	}

	policy := buildSandboxPolicy(state, pt)

	if policy["type"] != "readOnly" {
		t.Errorf("buildSandboxPolicy(verification).type = %v, want %q", policy["type"], "readOnly")
	}
	if roots, ok := policy["writableRoots"].([]string); !ok || len(roots) != 0 {
		t.Errorf("buildSandboxPolicy(verification).writableRoots = %v, want an empty slice", policy["writableRoots"])
	}
	if policy["networkAccess"] != false {
		t.Errorf("buildSandboxPolicy(verification).networkAccess = %v, want false", policy["networkAccess"])
	}

	workingState := &sessionState{target: agentcore.LaunchTarget{WorkspacePath: "/ws"}}
	workingPolicy := buildSandboxPolicy(workingState, pt)
	if workingPolicy["type"] != "danger-full-access" {
		t.Errorf("buildSandboxPolicy(working).type = %v, want the operator override %q", workingPolicy["type"], "danger-full-access")
	}
}

func recordThreadStart(t *testing.T, start domain.StartSessionParams, fake recordFramesParams) map[string]any {
	t.Helper()

	fake.RecordPath = filepath.Join(t.TempDir(), "frames")
	start.WorkspacePath = t.TempDir()
	start.AgentConfig = domain.AgentConfig{Command: agenttest.FakeRuntime(t, t.TempDir(), "codex", scenarioRecordFrames, fake)}
	start.Settings = map[string]any{"personality": "sentinel"}
	adapter := &CodexAdapter{}
	session, err := adapter.StartSession(context.Background(), start)
	if err != nil {
		t.Fatalf("StartSession(%+v) error = %v", start, err)
	}
	t.Cleanup(func() { _ = adapter.StopSession(context.Background(), session) })

	frames := (&recordingSession{recordPath: fake.RecordPath}).recorded(t, "thread/start")
	if len(frames) != 1 {
		t.Fatalf("StartSession(%+v) sent %d thread/start requests, want 1", start, len(frames))
	}
	return frames[0].Params
}

func TestStartThread_VerificationContext(t *testing.T) {
	t.Parallel()

	reduced := map[string]any{"project_doc_max_bytes": float64(0), "web_search": "disabled"}
	for feature := range strings.FieldsSeq("shell_tool unified_exec multi_agent apps plugins goals browser_use computer_use hooks in_app_browser") {
		reduced["features."+feature] = false
	}
	withServers := maps.Clone(reduced)
	withServers["mcp_servers.github.enabled"], withServers["mcp_servers.linear.enabled"] = false, false
	verify := domain.StartSessionParams{CredentialVerification: true}

	tests := []struct {
		name       string
		start      domain.StartSessionParams
		fake       recordFramesParams
		wantConfig map[string]any
	}{
		{"working thread", domain.StartSessionParams{}, recordFramesParams{}, nil},
		{"full-context verification", domain.StartSessionParams{CredentialVerification: true, VerificationFullContext: true}, recordFramesParams{}, nil},
		{"reduced verification", verify, recordFramesParams{ConfigReadServers: []string{"linear", "github"}}, withServers},
		{"reduced verification, config/read fails", verify, recordFramesParams{ConfigReadServers: []string{"github"}, ConfigReadFails: true}, reduced},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := recordThreadStart(t, tt.start, tt.fake)

			gotConfig, _ := got["config"].(map[string]any)
			if !maps.Equal(gotConfig, tt.wantConfig) {
				t.Errorf("thread/start(%s).config = %v, want %v", tt.name, gotConfig, tt.wantConfig)
			}
			wantBase, wantPersonality := any(nil), any("sentinel")
			if tt.wantConfig != nil {
				wantBase, wantPersonality = agentcore.VerificationSystemPrompt, nil
			}
			if got["baseInstructions"] != wantBase || got["personality"] != wantPersonality {
				t.Errorf("thread/start(%s) (baseInstructions, personality) = (%v, %v), want (%v, %v)", tt.name, got["baseInstructions"], got["personality"], wantBase, wantPersonality)
			}
		})
	}
}
