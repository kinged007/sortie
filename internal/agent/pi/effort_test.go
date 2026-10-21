package pi

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/typeutil"
)

func flagValue(args []string, flag string) string {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

func piEffortProbe() agenttest.EffortProbe {
	return func(t *testing.T, settings map[string]any, verification, resumed bool, turns int) ([]string, error) {
		t.Helper()

		dir := t.TempDir()
		command := agenttest.FakeRuntime(t, dir, "pi", agenttest.OutputScenario, agenttest.Output{})
		adapter, _ := NewPiAdapter()
		resumeID := ""
		if resumed {
			resumeID = "ses_probe"
		}
		session, err := adapter.StartSession(context.Background(), domain.StartSessionParams{
			WorkspacePath:          dir,
			AgentConfig:            domain.AgentConfig{Command: command},
			ResumeSessionID:        resumeID,
			CredentialVerification: verification,
			Settings:               maps.Clone(settings),
		})
		if err != nil {
			return nil, fmt.Errorf("StartSession: %w", err)
		}
		state := session.Internal.(*sessionState)

		carried := make([]string, 0, turns)
		for turn := 1; turn <= turns; turn++ {
			if turn > 1 {
				state.sessionID = "ses_probe"
			}
			carried = append(carried, flagValue(buildRunArgs(state, state.passthrough), "--thinking"))
		}
		return carried, nil
	}
}

func TestEffortForwarding(t *testing.T) {
	t.Parallel()

	agenttest.AssertEffortForwarding(t, piEffortProbe())
}

// startRefusedSession starts a session on a runtime that records any
// launch, and reports the start error and whether the runtime ran.
func startRefusedSession(t *testing.T, settings map[string]any) (error, bool) {
	t.Helper()

	dir := t.TempDir()
	marker := filepath.Join(dir, "launched")
	command := agenttest.WriteScript(t, dir, "fake-pi", "touch '"+marker+"'\necho 1.3.0\n")
	a, _ := NewPiAdapter()

	_, err := a.StartSession(context.Background(), domain.StartSessionParams{
		WorkspacePath: dir,
		AgentConfig:   domain.AgentConfig{Command: command},
		Settings:      settings,
	})
	_, statErr := os.Stat(marker)
	return err, statErr == nil
}

func hasCheck(diags []registry.ValidationDiag, check string) *registry.ValidationDiag {
	for i := range diags {
		if diags[i].Check == check {
			return &diags[i]
		}
	}
	return nil
}

func TestEffortThinkingConflict(t *testing.T) {
	t.Parallel()

	wantMessage := fmt.Sprintf("pi.%s and pi.thinking both set the thinking level; set one of them", registry.EffortKey)
	wantCheck := "pi." + registry.EffortKey + ".conflict"

	tests := []struct {
		name         string
		passthrough  map[string]any
		wantConflict bool
	}{
		{name: "effort and thinking both set", passthrough: map[string]any{registry.EffortKey: "high", "thinking": "low"}, wantConflict: true},
		{name: "effort alone", passthrough: map[string]any{registry.EffortKey: "high"}},
		{name: "thinking alone", passthrough: map[string]any{"thinking": "low"}},
		{name: "effort set and thinking empty", passthrough: map[string]any{registry.EffortKey: "high", "thinking": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			startErr, launched := startRefusedSession(t, tt.passthrough)
			diags := validateConfig(registry.AgentConfigFields{Kind: "pi", Passthrough: tt.passthrough})
			diag := hasCheck(diags, wantCheck)

			if !tt.wantConflict {
				if startErr != nil {
					t.Errorf("StartSession(%v) error = %v, want the settings accepted", tt.passthrough, startErr)
				}
				if diag != nil {
					t.Errorf("validateConfig(%v) reported %+v, want no %q", tt.passthrough, diag, wantCheck)
				}
				return
			}
			agentErr, ok := errors.AsType[*domain.AgentError](startErr)
			if !ok {
				t.Fatalf("StartSession(%v) error = %v, want the conflict refusal as a *domain.AgentError", tt.passthrough, startErr)
			}
			if diag == nil {
				t.Fatalf("validateConfig(%v) = %+v, want check %q", tt.passthrough, diags, wantCheck)
			}
			if agentErr.Message != wantMessage || diag.Message != wantMessage {
				t.Errorf("StartSession Message = %q, validateConfig Message = %q, want both %q", agentErr.Message, diag.Message, wantMessage)
			}
			if launched {
				t.Errorf("StartSession(%v) launched the runtime, want the refusal before any launch", tt.passthrough)
			}
		})
	}
}

func TestEffortWrongType(t *testing.T) {
	t.Parallel()

	passthrough := map[string]any{registry.EffortKey: 7}

	startErr, launched := startRefusedSession(t, passthrough)
	diags := validateConfig(registry.AgentConfigFields{Kind: "pi", Passthrough: passthrough})

	if _, ok := errors.AsType[*typeutil.TypeFault](startErr); !ok {
		t.Fatalf("StartSession(%v) error = %v, want a wrapped *typeutil.TypeFault", passthrough, startErr)
	}
	diag := hasCheck(diags, "pi."+registry.EffortKey+".wrong_type")
	if diag == nil {
		t.Fatalf("validateConfig(%v) = %+v, want the effort wrong_type check", passthrough, diags)
	}
	if agentErr, ok := errors.AsType[*domain.AgentError](startErr); !ok || diag.Message != agentErr.Message {
		t.Errorf("validateConfig Message = %q, want the text StartSession refused with: %v", diag.Message, startErr)
	}
	if launched {
		t.Error("StartSession launched the runtime, want the refusal before any launch")
	}
}
