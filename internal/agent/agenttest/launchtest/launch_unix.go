//go:build unix

package launchtest

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/sortie-ai/sortie/internal/agent/procutil"
)

// Held returns the held record of the launch whose direct child agentPID
// names, nil when no held launch has it, and an error naming agentPID when it
// is not a decimal above 0. Call it right after StartSession: once the launch
// is reaped, its number no longer names it.
func Held(agentPID string) (*procutil.Group, error) {
	pid, err := strconv.Atoi(agentPID)
	if err != nil || pid <= 0 {
		return nil, fmt.Errorf("agent pid %q is not a decimal process id above 0", agentPID)
	}
	return procutil.HeldGroup(pid), nil
}

// KillOnCleanup binds Held(agentPID) now and registers a cleanup that kills
// the launch's process group through the record; an error or a nil record
// registers nothing. Call it right after StartSession.
func KillOnCleanup(t testing.TB, agentPID string) {
	t.Helper()
	group, err := Held(agentPID)
	if err != nil || group == nil {
		return
	}
	t.Cleanup(func() { _ = group.Kill() })
}
