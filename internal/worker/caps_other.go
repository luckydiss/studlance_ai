//go:build !windows

package worker

// hasProgID is Windows-only (COM ProgIDs live in the registry); elsewhere no
// office suite is detected this way.
func hasProgID(progid string) bool {
	return false
}
