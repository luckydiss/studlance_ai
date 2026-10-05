//go:build windows

// Package procwin kills whole process trees spawned by agents: on Windows via
// a Job Object (children of children included), elsewhere via a process
// group. It also tracks Office/KOMPAS processes started through COM, which
// live outside the agent's process tree.
package procwin

import (
	"errors"
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

// Prepare makes the process start suspended, so it cannot spawn children in
// the gap between Start and Add assigning it to the job.
func (g *Group) Prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
}

// Add assigns the started (suspended) process to the job, then resumes its
// threads. The resume always runs, even when the assignment fails: a process
// left suspended forever is worse than a reported error.
func (g *Group) Add(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return fmt.Errorf("procwin: process not started")
	}
	pid := uint32(cmd.Process.Pid)

	g.mu.Lock()
	err := g.err
	if err == nil && g.closed {
		err = fmt.Errorf("procwin: job already closed")
	}
	if err == nil {
		var h windows.Handle
		h, err = windows.OpenProcess(
			windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SET_INFORMATION,
			false, pid)
		if err != nil {
			err = fmt.Errorf("procwin: OpenProcess: %w", err)
		} else {
			if aerr := windows.AssignProcessToJobObject(g.job, h); aerr != nil {
				err = fmt.Errorf("procwin: AssignProcessToJobObject: %w", aerr)
			}
			_ = windows.CloseHandle(h)
		}
	}
	g.mu.Unlock()

	if rerr := resumeProcessThreads(pid); rerr != nil && err == nil {
		err = rerr
	}
	return err
}

// resumeProcessThreads resumes every thread of a process started with
// CREATE_SUSPENDED. os/exec does not hand out the main thread handle, so the
// threads are found through a Toolhelp snapshot instead.
func resumeProcessThreads(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("procwin: thread snapshot: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	if err := windows.Thread32First(snap, &te); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil
		}
		return fmt.Errorf("procwin: Thread32First: %w", err)
	}
	var firstErr error
	for {
		if te.OwnerProcessID == pid {
			if err := resumeThread(te.ThreadID); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if err := windows.Thread32Next(snap, &te); err != nil {
			break
		}
	}
	return firstErr
}

// resumeThread fully resumes a thread: ResumeThread returns the previous
// suspend count, so it is called until the count reaches zero.
func resumeThread(tid uint32) error {
	h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, tid)
	if err != nil {
		return fmt.Errorf("procwin: OpenThread %d: %w", tid, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	for {
		prev, err := windows.ResumeThread(h)
		if err != nil {
			return fmt.Errorf("procwin: ResumeThread %d: %w", tid, err)
		}
		if prev <= 1 {
			return nil
		}
	}
}

// Kill terminates all job processes and closes the job handle. Idempotent.
func (g *Group) Kill() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.job == 0 {
		return
	}
	_ = windows.TerminateJobObject(g.job, 1)
	g.closeLocked()
}

// Close releases the job handle without terminating the processes. The job
// has KILL_ON_JOB_CLOSE, so closing the handle while an assigned process is
// still alive kills the whole tree: callers must Close only after the
// process has exited (e.g. deferred right after cmd.Wait). Idempotent.
func (g *Group) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closeLocked()
}

func (g *Group) closeLocked() {
	if g.closed {
		return
	}
	g.closed = true
	if g.job != 0 {
		_ = windows.CloseHandle(g.job)
		g.job = 0
	}
}
