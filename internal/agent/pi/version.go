package pi

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/mod/semver"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/redact"
)

// minimumVersion is the oldest pi release that has everything the adapter
// launches with: --exclude-tools (0.77.0), --approve and --no-approve
// (0.79.0), --extension, and pi.registerMcpServer with direct exposure
// (0.99.0).
const minimumVersion = "0.99.0"

// checkRuntimeVersion refuses a command that reports no readable version or
// a version older than [minimumVersion]. The query re-verifies the
// workspace, so a session start on a removed workspace fails here rather
// than at the first turn.
func checkRuntimeVersion(ctx context.Context, state *sessionState) *domain.AgentError {
	reported, agentErr := agentcore.QueryRuntimeVersion(ctx, state.target, state.agentConfig, state.logger(), withoutServerList(os.Environ()))
	if agentErr != nil {
		return agentErr
	}
	if reported.Version == "" {
		message := "no output"
		if reported.Line != "" {
			message = redact.Truncate(reported.Line, 200)
		}
		return &domain.AgentError{
			Kind:    domain.ErrAgentNotFound,
			Message: "the configured pi command reported no version Sortie can read: " + message,
		}
	}
	if semver.Compare("v"+reported.Version, "v"+minimumVersion) < 0 {
		return &domain.AgentError{
			Kind: domain.ErrAgentNotFound,
			Message: fmt.Sprintf("pi %s is not supported; install pi %s or later, published on npm as @earendil-works/pi-coding-agent",
				reported.Version, minimumVersion),
		}
	}
	return nil
}

// sessionFile names the file pi keeps a session in: one file per session
// under a directory per project, named by the session's start time and id.
func sessionFile(sessionID string) agentcore.RuntimePath {
	return agentcore.RuntimePath{
		HomeVar:     "PI_CODING_AGENT_DIR",
		HomeDefault: []string{".pi", "agent"},
		Segments:    []string{"sessions", "*", "*_" + sessionID + ".jsonl"},
	}
}
