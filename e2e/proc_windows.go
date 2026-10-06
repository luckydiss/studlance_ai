//go:build e2e && windows

package e2e

import "golang.org/x/sys/windows"

// processAlive reports whether pid still exists.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		_ = windows.CloseHandle(h)
		return false
	}
	_ = windows.CloseHandle(h)
	return code == 259 // STILL_ACTIVE
}
