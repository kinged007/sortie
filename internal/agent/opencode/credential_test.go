//go:build unix

package opencode

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/agent/agenttest/credentialtest"
	"github.com/sortie-ai/sortie/internal/domain"
)

func TestCredentialVerification(t *testing.T) {
	// Not parallel: t.Setenv carries the fake ssh stand-in on PATH.
	verifiedBin := writeRunFixtureScript(t, t.TempDir(), "simple_turn.jsonl")
	unverifiedBin := writeRunFixtureScript(t, t.TempDir(), "logical_failure_exit0.jsonl")

	adapter, err := NewOpenCodeAdapter()
	if err != nil {
		t.Fatalf("NewOpenCodeAdapter() error = %v", err)
	}

	credentialtest.AssertCredentialVerification(t, "opencode", credentialtest.RuntimeCases(t, adapter, domain.AgentConfig{}, verifiedBin, unverifiedBin, "opencode"))
}

func TestDeleteVerificationSession_CarriesManagedEnv(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, "env.txt")
	script := agenttest.WriteScript(t, tmpDir, "fake-opencode-delete",
		"printf '%s' \"$OPENCODE_DISABLE_AUTOUPDATE\" > '"+envPath+"'\n")
	state := &sessionState{
		target:     agentcore.LaunchTarget{Command: script, WorkspacePath: tmpDir},
		baseLogger: slog.Default(),
	}

	deleteVerificationSession(t.Context(), state, "ses_abc123")

	got, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile(env.txt): %v", err)
	}
	if string(got) != "true" {
		t.Errorf("session delete launch OPENCODE_DISABLE_AUTOUPDATE = %q, want %q", got, "true")
	}
}
