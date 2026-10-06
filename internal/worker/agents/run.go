package agents

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sync"
	"sync/atomic"
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
	// LogPath is the raw log: stdout lines as they are (any length), stderr
	// lines as {"_stderr":"…"}.
	LogPath       string
	ExpectSession string
	// Send delivers batches of new steps; nil is allowed (steps are only
	// parsed, never delivered). Send runs on its own goroutine, so blocking
	// inside it (network retries) never stalls the agent on a full pipe.
	// The first Send error kills the process tree and fails Run. Steps
	// arrive in Seq order with gaps where steps were dropped because the
	// queue memory limit was hit.
	Send func(steps []NewStep) error
	// OnSession is called exactly once with the codex thread id or the
	// confirmed claude session id, as soon as it is known. It runs on its
	// own goroutine so a slow callback cannot stall the reading; it must
	// not block for long, and Run does not wait for it to finish.
	OnSession func(id string)
}

// NewStep is a Step plus its run-wide sequence number, assigned by the
// runner when the step is queued, starting from 1.
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
	// KillTimeout is true when the process did not exit within
	// killWaitTimeout after the kill: its stdout/stderr pipes were closed
	// to unblock the readers and Wait was still awaited to the end.
	KillTimeout bool
	Usage       Usage
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

// maxQueueBytes caps the memory of the trace pipeline (approximated as
// summary length plus payload JSON length). The limit covers the queued steps
// AND the batch currently being sent together, so a stuck network cannot hold
// twice the cap. Steps parsed past the cap are dropped: they stay only in the
// raw log, but their Seq numbers are still consumed so the gaps are visible
// on the server. Package-level so tests can shrink it.
var maxQueueBytes = 50 << 20 // 50 MiB

// killWaitTimeout bounds how long Run waits for the process to exit after a
// kill before closing its pipes. Package-level so tests can shrink it.
var killWaitTimeout = 30 * time.Second

// parserLineLimit is the stdout line length above which the line is
// compacted by truncateJSONLine before parsing; the raw log always gets the
// full line.
const parserLineLimit = 1 << 20 // 1 MiB

// truncateBudget is the target size of a compacted parser line.
const truncateBudget = 900 * 1024 // ~900 KiB

// minTruncField is the smallest string field truncateJSONLine will cut.
const minTruncField = 1024

// Run starts the agent process, pumps its stdout through the parser, mirrors
// both streams into the raw log and delivers step batches until the process
// exits or ctx is done (then the whole process tree is killed).
//
// Reading (stdout/stderr, plus parsing) and sending (RunSpec.Send) run on
// separate goroutines: a stuck network never stalls the agent on a full
// pipe.
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

	// requestKill kills the whole tree exactly once: ctx cancellation and a
	// Send error both go through it. killReq arms the wait-timeout watcher.
	killReq := make(chan struct{})
	var killOnce sync.Once
	requestKill := func() {
		killOnce.Do(func() {
			group.Kill()
			close(killReq)
		})
	}
	var killTimeout atomic.Bool

	// Reap the process once both pipes are drained; Wait must not run while
	// the pipes are still being read.
	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	waitCh := make(chan error, 1)
	waitDone := make(chan struct{})
	go func() {
		<-stdoutDone
		<-stderrDone
		waitCh <- cmd.Wait()
		close(waitDone)
	}()

	// Kill the whole tree when the context is done; finished closes when Run
	// is about to return so the watchers never linger.
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			requestKill()
		case <-finished:
		}
	}()

	// Bound the wait after a kill: if the process still has not exited,
	// close the pipes so the readers and Wait can finish.
	go func() {
		select {
		case <-killReq:
		case <-finished:
			return
		}
		timer := time.NewTimer(killWaitTimeout)
		defer timer.Stop()
		select {
		case <-waitDone:
		case <-timer.C:
			killTimeout.Store(true)
			_ = stdout.Close()
			_ = stderr.Close()
		}
	}()

	queue := newStepQueue()

	// The stdout reader owns the parser: it logs every line in full
	// (however long), feeds compacted lines to the parser and queues new
	// steps with their Seq numbers. Steps are drained from the parser after
	// every line, so neither the parser nor the raw line buffers accumulate.
	go func() {
		br := bufio.NewReaderSize(stdout, 64<<10)
		seq := 0
		notified := false
		readLines(br, func(line []byte) {
			logLine(line)
			if len(line) > parserLineLimit {
				compact, ok := truncateJSONLine(line)
				if !ok {
					return // stays only in the raw log
				}
				line = compact
			}
			parser.Feed(line, time.Now().UTC())
			if !notified && spec.OnSession != nil {
				id := parser.ThreadID()
				if id == "" {
					id = parser.SessionID()
				}
				if id != "" {
					notified = true
					go spec.OnSession(id)
				}
			}
			for _, s := range parser.Drain() {
				seq++
				if spec.Send != nil {
					queue.push(NewStep{Seq: seq, Ts: s.Ts, Type: s.Type, Summary: s.Summary, Payload: s.Payload})
				}
			}
		})
		queue.close()
		close(stdoutDone)
	}()

	go func() {
		br := bufio.NewReaderSize(stderr, 64<<10)
		readLines(br, func(line []byte) {
			logStderr(string(line))
		})
		close(stderrDone)
	}()

	// The sender is the only caller of Send; a blocking Send never stalls
	// the readers.
	senderDone := make(chan struct{})
	var sendErr error
	if spec.Send != nil {
		go func() {
			defer close(senderDone)
			if err := queue.sendLoop(spec.Send); err != nil {
				sendErr = err
				requestKill()
			}
		}()
	} else {
		close(senderDone)
	}

	<-stdoutDone

	// Final flush: wait for the sender to drain the queue, unless ctx is
	// already done (then the process was killed; do not wait for the
	// network).
	flushed := false
	select {
	case <-senderDone:
		flushed = true
	case <-ctx.Done():
	}
	waitErr := <-waitCh
	// The process is dead: release the job/group handle (KILL_ON_JOB_CLOSE
	// would reap any stragglers spawned after the kill).
	group.Close()

	res := Result{Usage: parser.Usage(), KillTimeout: killTimeout.Load()}
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
	if flushed && sendErr != nil {
		return res, fmt.Errorf("agents: send steps: %w", sendErr)
	}
	return res, nil
}

// readLines reads r line by line with no length limit; fn gets every line
// (empty ones included) without the trailing newline.
func readLines(r *bufio.Reader, fn func(line []byte)) {
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			fn(bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'}))
		}
		if err != nil {
			return
		}
	}
}

// stepQueue is the in-memory buffer between the parsing reader and the Send
// goroutine. Its byte budget covers the queued steps and the batch currently
// in flight together: bytes stay accounted for until Send returns.
type stepQueue struct {
	mu     sync.Mutex
	signal chan struct{} // buffered 1: the batch is full or the queue closed
	steps  []NewStep
	sizes  []int
	bytes  int
	closed bool
	logged bool // the overflow loss was already logged
}

func newStepQueue() *stepQueue {
	return &stepQueue{signal: make(chan struct{}, 1)}
}

// stepSize estimates the heap a queued step holds.
func stepSize(s NewStep) int {
	return len(s.Summary) + len(payloadJSON(s.Payload))
}

// push appends a step unless the queue plus the in-flight batch is over
// maxQueueBytes; the loss is logged once to the runner's stderr (dropped
// steps stay in the raw log, their Seq numbers are still consumed).
func (q *stepQueue) push(s NewStep) {
	size := stepSize(s)
	q.mu.Lock()
	if q.bytes+size > maxQueueBytes {
		if !q.logged {
			q.logged = true
			_, _ = fmt.Fprintf(os.Stderr,
				"agents: send queue over %d bytes, dropping steps (they stay in the raw log; Seq gaps are expected)\n",
				maxQueueBytes)
		}
		q.mu.Unlock()
		return
	}
	q.bytes += size
	q.steps = append(q.steps, s)
	q.sizes = append(q.sizes, size)
	full := len(q.steps) >= flushBatchSize
	q.mu.Unlock()
	if full {
		q.notify()
	}
}

// take returns and removes all pending steps with their sizes; closed reports
// whether the reader is done and no more steps will come. The bytes remain
// accounted for until release, so the caller must release the batch.
func (q *stepQueue) take() (batch []NewStep, sizes []int, closed bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	batch, sizes = q.steps, q.sizes
	q.steps, q.sizes = nil, nil
	return batch, sizes, q.closed
}

// release frees the byte accounting of a batch returned by take; it is called
// once Send has returned (successfully or not), when the batch is garbage.
func (q *stepQueue) release(sizes []int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, size := range sizes {
		q.bytes -= size
	}
	if q.bytes < 0 {
		q.bytes = 0
	}
}

// close marks the queue complete and wakes the sender for the final flush.
func (q *stepQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.notify()
}

func (q *stepQueue) notify() {
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// sendLoop flushes queued steps through send until the queue is closed and
// drained: a batch goes out every flushInterval or once flushBatchSize
// steps are pending, whichever comes first. send may block arbitrarily long;
// its batch keeps counting against maxQueueBytes until it returns, so the
// queue never grows past the cap together with it.
func (q *stepQueue) sendLoop(send func([]NewStep) error) error {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		batch, sizes, closed := q.take()
		if len(batch) > 0 {
			err := send(batch)
			q.release(sizes)
			if err != nil {
				return err
			}
			continue
		}
		if closed {
			return nil
		}
		select {
		case <-q.signal:
		case <-ticker.C:
		}
	}
}

// payloadJSON sizes a payload for the queue memory estimate.
func payloadJSON(payload map[string]interface{}) []byte {
	if payload == nil {
		return nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return b
}

// truncateJSONLine compacts an over-long stdout line for the parser: the
// longest string fields (at any nesting level) keep only their tails until
// the whole line roughly fits truncateBudget, and a "_truncated" note
// records how many bytes were removed. ok is false when the line is not a
// JSON object; such lines are not fed to the parser at all.
func truncateJSONLine(line []byte) (compact []byte, ok bool) {
	var doc map[string]interface{}
	if err := json.Unmarshal(line, &doc); err != nil {
		return nil, false
	}

	// field is one long string found in the document; set writes its
	// truncated value back into the parent container.
	type field struct {
		s   string
		set func(string)
	}
	var fields []field
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch v := v.(type) {
		case map[string]interface{}:
			for k, e := range v {
				if s, ok := e.(string); ok {
					if len(s) >= minTruncField {
						fields = append(fields, field{s: s, set: func(n string) { v[k] = n }})
					}
					continue
				}
				walk(e)
			}
		case []interface{}:
			for i, e := range v {
				if s, ok := e.(string); ok {
					if len(s) >= minTruncField {
						fields = append(fields, field{s: s, set: func(n string) { v[i] = n }})
					}
					continue
				}
				walk(e)
			}
		}
	}
	walk(doc)
	slices.SortFunc(fields, func(a, b field) int { return cmp.Compare(len(b.s), len(a.s)) })

	removed := 0
	excess := len(line) - truncateBudget
	for _, f := range fields {
		if excess <= 0 {
			break
		}
		keep := len(f.s) - excess
		if keep < minTruncField {
			keep = minTruncField
		}
		if keep >= len(f.s) {
			continue
		}
		// The tail usually carries the interesting part (the error); the
		// cut lands on a rune boundary.
		t := tailBytes(f.s, keep)
		removed += len(f.s) - len(t)
		excess -= len(f.s) - len(t)
		f.set(t)
	}
	doc["_truncated"] = fmt.Sprintf("…обрезано %d байт", removed)
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, false
	}
	return out, true
}
