//go:build unix

package waittest

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// processExited counts a zombie as exited: an unreaped child still answers
// signal 0, and procfs is the only place that shows it has died.
func processExited(pid int) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return true
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	stat := string(data)
	// The command name is parenthesized and may itself hold ")", so the state
	// follows the last one.
	i := strings.LastIndex(stat, ")")
	return i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z'
}
