package waittest

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// File returns the content of path once os.ReadFile succeeds; an empty file
// counts. It fails the test when the file stays unreadable for HangGuard.
func File(t testing.TB, path string) []byte {
	t.Helper()
	var data []byte
	Until(t, func() error {
		var err error
		data, err = os.ReadFile(path) //nolint:gosec // G304: path is the caller's own test fixture
		return err
	})
	return data
}

// PIDFile returns the process ID path holds once its trimmed content parses
// above 0, so a file written in two steps is waited through. It fails the test
// when no such content appears within HangGuard.
func PIDFile(t testing.TB, path string) int {
	t.Helper()
	var pid int
	Until(t, func() error {
		data, err := os.ReadFile(path) //nolint:gosec // G304: path is the caller's own test fixture
		if err != nil {
			return err
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return fmt.Errorf("pid file %s: %w", path, err)
		}
		if n <= 0 {
			return fmt.Errorf("pid file %s holds %d, want above 0", path, n)
		}
		pid = n
		return nil
	})
	return pid
}

// ProcessGone returns once pid has exited, reaped or not, and fails the test
// when it is still running after HangGuard. It returns at once for pid <= 0.
// A caller that must tell a reaped process from a zombie checks that itself.
func ProcessGone(t testing.TB, pid int) {
	t.Helper()
	if pid <= 0 {
		return
	}
	Until(t, func() error {
		if processExited(pid) {
			return nil
		}
		return fmt.Errorf("process %d is still running", pid)
	})
}
