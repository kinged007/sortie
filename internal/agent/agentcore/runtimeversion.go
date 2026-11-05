package agentcore

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/sortie-ai/sortie/internal/agent/procutil"
	"github.com/sortie-ai/sortie/internal/agent/sshutil"
	"github.com/sortie-ai/sortie/internal/domain"
)

// RuntimeVersion is what a runtime's --version output reports.
type RuntimeVersion struct {
	Version string // empty when the output carries no readable version
	Major   int
	Line    string // the output's first readable line, "" when it has none
}

// QueryRuntimeVersion runs the session's command with --version through
// [LaunchTarget.AuxiliaryCommand], locally or over ssh like the session,
// bounded by [AuxiliaryTimeout]. A launch that fails to start, outlives the
// bound or exits non-zero returns the matching [*domain.AgentError]; output
// carrying no readable version, or cut off by the capture limit, returns an
// empty Version for the caller to judge. env and carried are as for
// AuxiliaryCommand.
func QueryRuntimeVersion(ctx context.Context, target LaunchTarget, cfg domain.AgentConfig, logger *slog.Logger, env []string, carried ...sshutil.EnvVar) (RuntimeVersion, *domain.AgentError) {
	if logger == nil {
		logger = slog.Default()
	}
	queryCtx, cancel := context.WithTimeout(ctx, AuxiliaryTimeout(cfg))
	defer cancel()

	stdout := procutil.NewTailBuffer(EarlyExitCaptureBytes)
	stderr := procutil.NewTailBuffer(EarlyExitCaptureBytes)

	cmd, agentErr := target.AuxiliaryCommand(queryCtx, []string{"--version"}, nil, env, carried...)
	if agentErr != nil {
		return RuntimeVersion{}, agentErr
	}

	result, startErr := procutil.RunCapture(queryCtx, cmd, procutil.StopGrace(cfg.StopGraceMS), procutil.CaptureParams{
		Stdout: stdout,
		Stderr: stderr,
		Logger: logger,
	})

	switch {
	case startErr != nil:
		if cancelled := SessionStartCancelledError(ctx, startErr); cancelled != nil {
			return RuntimeVersion{}, cancelled
		}
		return RuntimeVersion{}, &domain.AgentError{
			Kind:    domain.ErrResponseError,
			Message: "could not start the agent runtime to read its version",
			Err:     startErr,
		}
	case ctx.Err() != nil:
		return RuntimeVersion{}, &domain.AgentError{
			Kind:    domain.ErrResponseError,
			Message: "the session start ended while the agent runtime was reporting its version",
			Err:     ctx.Err(),
		}
	case queryCtx.Err() != nil:
		return RuntimeVersion{}, &domain.AgentError{
			Kind: domain.ErrResponseTimeout,
			Message: fmt.Sprintf("the agent runtime did not report its version within %d ms",
				AuxiliaryTimeout(cfg).Milliseconds()),
		}
	case result.WaitErr != nil:
		if target.RemoteCommand != "" && sshutil.ConnectionFailed(procutil.ExtractExitCode(result.WaitErr)) {
			return RuntimeVersion{}, ConnectionFailedError()
		}
		return RuntimeVersion{}, ExitedEarly(target, result).Report(stderr.Collector(logger))
	}

	reported := RuntimeVersion{Line: FirstReadableLine(stdout.Bytes())}
	if !stdout.Truncated() {
		reported.Version, reported.Major, _ = ParseRuntimeVersion(stdout.Bytes())
	}
	logger.Debug("agent runtime version read", slog.String("version", reported.Line))
	return reported, nil
}

// ParseRuntimeVersion extracts the version and major number from a runtime's
// --version output. ok is false when no whitespace-separated field of the
// first readable line is a full semantic version once one leading "v" and
// one trailing "." are dropped.
func ParseRuntimeVersion(out []byte) (version string, major int, ok bool) {
	for field := range strings.FieldsSeq(FirstReadableLine(out)) {
		candidate := strings.TrimSuffix(strings.TrimPrefix(field, "v"), ".")
		prefixed := "v" + candidate
		if !isFullSemver(prefixed) {
			continue
		}
		majorInt, err := strconv.Atoi(strings.TrimPrefix(semver.Major(prefixed), "v"))
		if err != nil {
			return "", 0, false
		}
		return candidate, majorInt, true
	}
	return "", 0, false
}

// isFullSemver reports whether version, with its leading "v", is valid
// semver that spells out major, minor and patch. semver.IsValid alone
// also accepts shorthand such as "v1.18".
func isFullSemver(version string) bool {
	return semver.Canonical(version) == strings.TrimSuffix(version, semver.Build(version))
}

// FirstReadableLine returns the first line of out that is non-empty after
// [SanitizeLine] and trimming, or "" when every line is empty.
func FirstReadableLine(out []byte) string {
	for raw := range strings.SplitSeq(string(out), "\n") {
		line := strings.TrimSpace(SanitizeLine(raw))
		if line != "" {
			return line
		}
	}
	return ""
}
