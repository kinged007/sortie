package opencode

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/agenttest"
	"github.com/sortie-ai/sortie/internal/domain"
)

func TestStartSession_RefusesUnsupportedMajor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		reportedVersion string
		refusedVersion  string
	}{
		{name: "major 1", reportedVersion: "1.18.33", refusedVersion: "1.18.33"},
		{name: "major 0 development build", reportedVersion: "0.0.0-dev-202609250101", refusedVersion: "0.0.0-dev-202609250101"},
		{name: "major 3", reportedVersion: "3.0.0", refusedVersion: "3.0.0"},
		{name: "major 2 starts", reportedVersion: "opencode v2.0.18"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			command := agenttest.FakeRuntime(t, dir, "opencode", agenttest.OutputScenario, agenttest.Output{Version: tt.reportedVersion})
			a, err := NewOpenCodeAdapter()
			if err != nil {
				t.Fatalf("NewOpenCodeAdapter() error = %v", err)
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
			if err == nil {
				t.Fatalf("StartSession(version %q) = session %+v, error nil, want an agent_not_found refusal", tt.reportedVersion, session)
			}
			var agentErr *domain.AgentError
			if !errors.As(err, &agentErr) {
				t.Fatalf("StartSession(version %q) error = %v (%T), want a *domain.AgentError", tt.reportedVersion, err, err)
			}
			if agentErr.Kind != domain.ErrAgentNotFound {
				t.Errorf("StartSession(version %q) Kind = %q, want %q", tt.reportedVersion, agentErr.Kind, domain.ErrAgentNotFound)
			}
			wantMessage := fmt.Sprintf("OpenCode %s is not supported; install OpenCode 2.x, published on npm as @opencode/cli", tt.refusedVersion)
			if agentErr.Message != wantMessage {
				t.Errorf("StartSession(version %q) Message = %q, want %q", tt.reportedVersion, agentErr.Message, wantMessage)
			}
			if session.Internal != nil {
				t.Errorf("StartSession(version %q) session.Internal = %v, want no session", tt.reportedVersion, session.Internal)
			}
		})
	}
}
