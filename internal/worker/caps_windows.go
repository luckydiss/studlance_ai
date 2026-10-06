//go:build windows

package worker

import "golang.org/x/sys/windows/registry"

// hasProgID reports whether a COM ProgID exists under HKCR.
func hasProgID(progid string) bool {
	k, err := registry.OpenKey(registry.CLASSES_ROOT, progid, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	_ = k.Close()
	return true
}
