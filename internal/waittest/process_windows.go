//go:build windows

package waittest

import (
	"math"

	"golang.org/x/sys/windows"
)

func processExited(pid int) bool {
	if pid < 0 || pid > math.MaxUint32 {
		return true
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true
	}
	event, waitErr := windows.WaitForSingleObject(handle, 0)
	_ = windows.CloseHandle(handle)
	return waitErr == nil && event == uint32(windows.WAIT_OBJECT_0)
}
