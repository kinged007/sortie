package opencode

import (
	"context"
	"fmt"
	"os"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/agent/sshutil"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/redact"
)

// checkRuntimeVersion refuses a command that reports no readable version or
// a major other than 2. The query re-verifies the workspace, so a session
// start on a removed workspace fails here rather than at the first turn.
func checkRuntimeVersion(ctx context.Context, state *sessionState) *domain.AgentError {
	reported, agentErr := agentcore.QueryRuntimeVersion(ctx, state.target, state.agentConfig, state.logger(), versionQueryEnv(os.Environ()),
		sshutil.EnvVar{Name: "OPENCODE_DISABLE_AUTOUPDATE", Value: "true"})
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
			Message: "the configured OpenCode command reported no version Sortie can read: " + message,
		}
	}
	if reported.Major != 2 {
		return &domain.AgentError{
			Kind:    domain.ErrAgentNotFound,
			Message: fmt.Sprintf("OpenCode %s is not supported; install OpenCode 2.x, published on npm as @opencode/cli", reported.Version),
		}
	}
	return nil
}

// versionQueryEnv scrubs base of every managed variable a turn's own
// environment carries and appends the one setting the version query
// itself needs, so the query never carries a tool policy or a sharing
// setting.
func versionQueryEnv(base []string) []string {
	env := make([]string, 0, len(base)+1)
	for _, entry := range base {
		if shouldDropManagedEnv(entry) {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "OPENCODE_DISABLE_AUTOUPDATE=true")
}
