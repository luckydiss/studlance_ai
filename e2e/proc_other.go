//go:build e2e && !windows

package e2e

import "syscall"

// processAlive reports whether pid still exists.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
