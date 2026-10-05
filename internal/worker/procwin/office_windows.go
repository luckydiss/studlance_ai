//go:build windows

package procwin

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// officeExes are process names (lowercase) that COM automation starts outside
// the agent's process tree: KOMPAS, its student edition and the Office apps.
var officeExes = map[string]bool{
	"kompas.exe":   true,
	"kstudy.exe":   true,
	"winword.exe":  true,
	"excel.exe":    true,
	"powerpnt.exe": true,
	"soffice.bin":  true,
}

// OfficePIDs returns the PIDs of running KOMPAS/Office processes.
func OfficePIDs() []uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return nil
	}
	var pids []uint32
	for {
		name := strings.ToLower(windows.UTF16ToString(pe.ExeFile[:]))
		if officeExes[name] {
			pids = append(pids, pe.ProcessID)
		}
		if err := windows.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	return pids
}

// KillNewOffice terminates KOMPAS/Office processes that are not in before
// (typically a snapshot taken before the stage started).
func KillNewOffice(before []uint32) {
	keep := make(map[uint32]bool, len(before))
	for _, pid := range before {
		keep[pid] = true
	}
	for _, pid := range OfficePIDs() {
		if keep[pid] {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
		if err != nil {
			continue
		}
		_ = windows.TerminateProcess(h, 1)
		_ = windows.CloseHandle(h)
	}
}
