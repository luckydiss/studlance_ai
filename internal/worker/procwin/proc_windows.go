//go:build windows

// Package procwin kills whole process trees spawned by agents: on Windows via
// a Job Object (children of children included), elsewhere via a process
// group. It also tracks Office/KOMPAS processes started through COM, which
// live outside the agent's process tree.
package procwin

import (
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobObjectLimitKillOnJobClose kills all job processes when the handle closes.
const jobObjectLimitKillOnJobClose = 0x2000

// Group is a Windows Job Object: every assigned process dies on Kill, no
// matter how deep it sits in the process tree.
type Group struct {
	mu     sync.Mutex
	job    windows.Handle
	err    error // CreateJobObject failure, surfaced by Add
	closed bool
}

// NewGroup creates a Job Object with KILL_ON_JOB_CLOSE.
func NewGroup() *Group {
	g := &Group{}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		g.err = fmt.Errorf("procwin: CreateJobObject: %w", err)
		return g
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	_, err = windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)))
	if err != nil {
		_ = windows.CloseHandle(job)
		g.err = fmt.Errorf("procwin: SetInformationJobObject: %w", err)
		return g
	}
	g.job = job
	return g
}

// Prepare is a no-op on Windows: processes join the job after Start.
func (g *Group) Prepare(cmd *exec.Cmd) {}

// Add assigns the started process to the job. It must be called right after
// cmd.Start, before the process can spawn children on its own.
func (g *Group) Add(cmd *exec.Cmd) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	if g.closed {
		return fmt.Errorf("procwin: job already closed")
	}
	if cmd.Process == nil {
		return fmt.Errorf("procwin: process not started")
	}
	h, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SET_INFORMATION,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("procwin: OpenProcess: %w", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.AssignProcessToJobObject(g.job, h); err != nil {
		return fmt.Errorf("procwin: AssignProcessToJobObject: %w", err)
	}
	return nil
}

// Kill terminates all job processes and closes the handle. It is idempotent.
func (g *Group) Kill() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.job == 0 {
		return
	}
	g.closed = true
	_ = windows.TerminateJobObject(g.job, 1)
	_ = windows.CloseHandle(g.job)
}
