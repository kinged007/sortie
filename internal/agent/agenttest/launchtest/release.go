// Package launchtest binds, kills and judges a launch's procutil.Group record
// for tests and the qualification tool.
//
// It is a child of agenttest because it imports procutil, whose in-package
// tests import agenttest: agenttest must not import procutil, directly or
// transitively, or those tests fail with an import cycle.
package launchtest

import (
	"errors"
	"fmt"
)

// ErrNotReleased reports a record its reap has not released yet.
var ErrNotReleased = errors.New("the launch's process group is not released")

// ReleaseError judges a record's Released result: nil when released with no
// cleanup error, ErrNotReleased when not released, otherwise an error wrapping
// cleanupErr.
func ReleaseError(released bool, cleanupErr error) error {
	if !released {
		return ErrNotReleased
	}
	if cleanupErr != nil {
		return fmt.Errorf("the launch's process group was released with a cleanup error: %w", cleanupErr)
	}
	return nil
}
