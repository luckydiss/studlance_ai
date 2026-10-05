package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/luckydiss/studlance_ai/internal/worker/procwin"
)

// RunSpec describes one agent process to run.
type RunSpec struct {
	Agent   string // "codex" | "claude"
	Command string
	Args    []string
	Dir     string // cwd of the process
	Prompt  string // written to stdin (UTF-8), then stdin is closed
	// LogPath is the raw log: stdout lines as they are, stderr lines as
	// {"_stderr":"…"}.
	LogPath       string
	ExpectSession string
	// Send delivers batches of new steps; nil is allowed (steps are only
	// parsed, never delivered).
	Send func(steps []NewStep) error
	// OnSession is called exactly once with the codex thread id or the
	// confirmed claude session id, as soon as it is known.
	OnSession func(id string)
}

// NewStep is a Step plus its run-wide sequence number, assigned by the
// runner at send time, starting from 1.
type NewStep struct {
	Seq     int
	Ts      time.Time
	Type    string
	Summary string
	Payload map[string]interface{}
}

// Result is the outcome of a finished (or killed) agent run.
type Result struct {
	ExitCode int
	// Killed is true when the process was killed because ctx was done.
	Killed bool
	Usage  Usage
	// AgentFailed is non-empty when the agent itself reported a failure.
	AgentFailed string
}

// flushInterval and flushBatchSize control how often parsed steps go out
// through RunSpec.Send: every flushInterval or every flushBatchSize pending
// steps, whichever comes first. Package-level so tests can shrink them.
var (
	flushInterval  = 2 * time.Second
	flushBatchSize = 50
)

// scanBufSize is the bufio.Scanner capacity for agent output lines; codex
// aggregated_output and claude tool results can be large.
const scanBufSize = 1 << 20 // 1 MiB

// Run starts the agent process, pumps its stdout through the parser, mirrors
// both streams into the raw log and delivers step batches until the process
// exits or ctx is done (then the whole process tree is killed).
func Run(ctx context.Context, spec RunSpec) (Result, error) {
	var parser Parser
	switch spec.Agent {
	case "codex":
		parser = NewCodexParser()
	case "claude":
		parser = NewClaudeParser(spec.ExpectSession)
	default:
		return Result{}, fmt.Errorf("agents: unknown agent %q", spec.Agent)
	}

	logFile, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Result{}, fmt.Errorf("agents: open log %s: %w", spec.LogPath, err)
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("agents: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("agents: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, fmt.Errorf("agents: stderr pipe: %w", err)
	}

	group := procwin.NewGroup()
	group.Prepare(cmd)
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("agents: start %s: %w", spec.Command, err)
	}
	if err := group.Add(cmd); err != nil {
		_ = cmd.Process.Kill()
		return Result{}, fmt.Errorf("agents: add to group: %w", err)
	}

	// The prompt goes to stdin, then stdin is closed so the agent starts.
	go func() {
		_, _ = io.WriteString(stdin, spec.Prompt)
		_ = stdin.Close()
	}()

	var logMu sync.Mutex
	logLine := func(line []byte) {
		logMu.Lock()
		defer logMu.Unlock()
		_, _ = logFile.Write(line)
		_, _ = logFile.Write([]byte{'\n'})
	}
	logStderr := func(line string) {
		b, err := json.Marshal(map[string]string{"_stderr": line})
		if err == nil {
			logLine(b)
		}
	}

	lines := make(chan []byte, 16)
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		defer close(lines)
		scanLines(stdout, scanBufSize, func(line []byte) { lines <- line })
	}()

	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanLines(stderr, scanBufSize, func(line []byte) { logStderr(string(line)) })
	}()

	// Reap the process once both pipes are drained; Wait must not run while
	// the pipes are still being read.
	waitCh := make(chan error, 1)
	go func() {
		<-stdoutDone
		<-stderrDone
		waitCh <- cmd.Wait()
	}()

	// Kill the whole tree when the context is done; finished closes when Run
	// is about to return so the watcher never lingers.
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			group.Kill()
		case <-finished:
		}
	}()

	var sendErr error
	sent := 0
	notified := false
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	flush := func() {
		if sendErr != nil {
			return
		}
		steps := parser.Steps()
		if len(steps) == sent {
			return
		}
		batch := make([]NewStep, 0, len(steps)-sent)
		for i, s := range steps[sent:] {
			batch = append(batch, NewStep{
				Seq:     sent + i + 1,
				Ts:      s.Ts,
				Type:    s.Type,
				Summary: s.Summary,
				Payload: s.Payload,
			})
		}
		if spec.Send != nil {
			if err := spec.Send(batch); err != nil {
				sendErr = err
				group.Kill()
				return
			}
		}
		sent = len(steps)
	}

loop:
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				break loop
			}
			ts := time.Now().UTC()
			logLine(line)
			parser.Feed(line, ts)
			if !notified && spec.OnSession != nil {
				id := parser.ThreadID()
				if id == "" {
					id = parser.SessionID()
				}
				if id != "" {
					notified = true
					spec.OnSession(id)
				}
			}
			if len(parser.Steps())-sent >= flushBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
	var waitErr error
	if sendErr != nil {
		// The process was killed; do not wait for it forever.
		select {
		case waitErr = <-waitCh:
		case <-time.After(10 * time.Second):
			return Result{}, fmt.Errorf("agents: send steps: %w (process still running after kill)", sendErr)
		}
	} else {
		waitErr = <-waitCh
	}
	flush()

	res := Result{Usage: parser.Usage()}
	if msg, ok := parser.Failed(); ok {
		res.AgentFailed = msg
	}
	res.Killed = ctx.Err() != nil

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			return res, fmt.Errorf("agents: wait: %w", waitErr)
		}
	}
	if sendErr != nil {
		return res, fmt.Errorf("agents: send steps: %w", sendErr)
	}
	return res, nil
}

// scanLines reads r line by line with a big buffer; lines longer than the
// buffer stop the scan silently (they stay incomplete in the raw log).
func scanLines(r io.Reader, bufSize int, fn func(line []byte)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, bufSize), bufSize*4)
	for sc.Scan() {
		line := make([]byte, len(sc.Bytes()))
		copy(line, sc.Bytes())
		fn(line)
	}
}
