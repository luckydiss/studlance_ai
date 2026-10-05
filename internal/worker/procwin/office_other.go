//go:build !windows

package procwin

// OfficePIDs is Windows-only (COM automation); elsewhere there is nothing to
// track.
func OfficePIDs() []uint32 {
	return nil
}

// KillNewOffice is a no-op outside Windows.
func KillNewOffice(before []uint32) {}
