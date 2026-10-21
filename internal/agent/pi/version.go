package pi

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/agent/procutil"
	"github.com/sortie-ai/sortie/internal/agent/sshutil"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/redact"
)

// minimumVersion is the oldest pi release that has everything the adapter
// launches with: --exclude-tools (0.77.0), --approve and --no-approve
// (0.79.0), --extension, and pi.registerMcpServer with direct exposure
// (0.99.0).
var minimumVersion = piVersion{major: 0, minor: 99, patch: 0}

// piVersionPattern matches a bare semantic version, with an optional
// pre-release or build suffix kept but not decomposed.
var piVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)([-+][0-9A-Za-z.+-]+)?$`)

type piVersion struct {
	major, minor, patch int
}

func (v piVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

func (v piVersion) before(other piVersion) bool {
	if v.major != other.major {
		return v.major < other.major
	}
	if v.minor != other.minor {
		return v.minor < other.minor
	}
	return v.patch < other.patch
}

// checkRuntimeVersion launches the configured command with --version,
// bounded by [agentcore.AuxiliaryTimeout], and refuses a command that
// reports no readable version or a version older than [minimumVersion].
// It re-verifies the workspace through the same
// [agentcore.LaunchTarget.AuxiliaryCommand] path every other auxiliary
// launch uses, so a session start on a removed workspace fails here
// rather than at the first turn.
func checkRuntimeVersion(ctx context.Context, state *sessionState) *domain.AgentError {
	queryCtx, cancel := context.WithTimeout(ctx, agentcore.AuxiliaryTimeout(state.agentConfig))
	defer cancel()

	stdout := procutil.NewTailBuffer(agentcore.EarlyExitCaptureBytes)
	stderr := procutil.NewTailBuffer(agentcore.EarlyExitCaptureBytes)

	cmd, agentErr := state.target.AuxiliaryCommand(queryCtx, []string{"--version"}, nil, versionQueryEnv(os.Environ()))
	if agentErr != nil {
		return agentErr
	}

	result, startErr := procutil.RunCapture(queryCtx, cmd, procutil.StopGrace(state.agentConfig.StopGraceMS), procutil.CaptureParams{
		Stdout: stdout,
		Stderr: stderr,
		Logger: state.logger(),
	})

	switch {
	case startErr != nil:
		if cancelled := agentcore.SessionStartCancelledError(ctx, startErr); cancelled != nil {
			return cancelled
		}
		return &domain.AgentError{
			Kind:    domain.ErrResponseError,
			Message: "could not start the agent runtime to read its version",
			Err:     startErr,
		}
	case ctx.Err() != nil:
		return &domain.AgentError{
			Kind:    domain.ErrResponseError,
			Message: "the session start ended while the agent runtime was reporting its version",
			Err:     ctx.Err(),
		}
	case queryCtx.Err() != nil:
		return &domain.AgentError{
			Kind: domain.ErrResponseTimeout,
			Message: fmt.Sprintf("the agent runtime did not report its version within %d ms",
				agentcore.AuxiliaryTimeout(state.agentConfig).Milliseconds()),
		}
	case result.WaitErr != nil:
		if state.target.RemoteCommand != "" && sshutil.ConnectionFailed(procutil.ExtractExitCode(result.WaitErr)) {
			return agentcore.ConnectionFailedError()
		}
		return agentcore.ExitedEarly(state.target, result).Report(stderr.Collector(state.logger()))
	}

	version, ok := parseRuntimeVersion(stdout.Bytes(), stdout.Truncated())
	if !ok {
		message := "no output"
		if line := firstReadableLine(stdout.Bytes()); line != "" {
			message = redact.Truncate(line, 200)
		}
		return &domain.AgentError{
			Kind:    domain.ErrAgentNotFound,
			Message: "the configured pi command reported no version Sortie can read: " + message,
		}
	}
	if version.before(minimumVersion) {
		return &domain.AgentError{
			Kind: domain.ErrAgentNotFound,
			Message: fmt.Sprintf("pi %s is not supported; install pi %s or later, published on npm as @earendil-works/pi-coding-agent",
				version, minimumVersion),
		}
	}

	return nil
}

// versionQueryEnv scrubs base of every variable a turn's own environment
// manages, so the query never carries a bridge's server list.
func versionQueryEnv(base []string) []string {
	env := make([]string, 0, len(base))
	for _, entry := range base {
		if shouldDropManagedEnv(entry) {
			continue
		}
		env = append(env, entry)
	}
	return env
}

// parseRuntimeVersion extracts the version from a version query's
// standard output. ok is false when out is truncated or carries no field,
// among the first non-empty line's whitespace-separated fields, that
// (after dropping one leading "v") matches a bare semantic version.
func parseRuntimeVersion(out []byte, truncated bool) (version piVersion, ok bool) {
	if truncated {
		return piVersion{}, false
	}

	line := firstReadableLine(out)
	if line == "" {
		return piVersion{}, false
	}

	for field := range strings.FieldsSeq(line) {
		match := piVersionPattern.FindStringSubmatch(strings.TrimPrefix(field, "v"))
		if match == nil {
			continue
		}
		parts := [3]int{}
		for i := range parts {
			n, err := strconv.Atoi(match[i+1])
			if err != nil {
				return piVersion{}, false
			}
			parts[i] = n
		}
		return piVersion{major: parts[0], minor: parts[1], patch: parts[2]}, true
	}
	return piVersion{}, false
}

// firstReadableLine returns the first line of out that is non-empty
// after [agentcore.SanitizeLine] and trimming, or "" when every line is
// empty.
func firstReadableLine(out []byte) string {
	for raw := range strings.SplitSeq(string(out), "\n") {
		line := strings.TrimSpace(agentcore.SanitizeLine(raw))
		if line != "" {
			return line
		}
	}
	return ""
}
