//go:build !windows

// Package procwin kills whole process trees spawned by agents: on Windows via
// a Job Object (children of children included), elsewhere via a process
// group. It also tracks Office/KOMPAS processes started through COM, which
// live outside the agent's process tree.
package procwin

import (
	"os/exec"
	"sync"
	"syscall"
)

// Group is a process group: Prepare puts the child into its own group and
// Kill signals the whole group.
type Group struct {
	mu     sync.Mutex
	pgid   int
	killed bool
}

// NewGroup returns an empty group; the pgid is learned from the first Add.
func NewGroup() *Group {
	return &Group{}
}

// Prepare makes the child a process group leader (pgid = its pid).
func (g *Group) Prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Add remembers the started process as the group to kill.
func (g *Group) Add(cmd *exec.Cmd) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if cmd.Process != nil && g.pgid == 0 {
		g.pgid = cmd.Process.Pid
	}
	return nil
}

// Kill sends SIGKILL to the process group. It is idempotent.
func (g *Group) Kill() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.killed || g.pgid == 0 {
		return
	}
	g.killed = true
	_ = syscall.Kill(-g.pgid, syscall.SIGKILL)
}

// Close is a no-op outside Windows: Kill signals the group and there is no
// handle to release. It exists so callers can defer it unconditionally.
func (g *Group) Close() {}
