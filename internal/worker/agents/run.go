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
	"reflect"
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

// maxQueueBytes caps the memory of the trace pipeline. The estimate charges
// every step a conservative fixed cost (see stepSize), so it covers the
// held NewStep structs, the slice capacities, the payload containers and the
// summary bytes. The limit covers the queued steps AND the batch currently
// being sent together, so a stuck network cannot hold twice the cap. Steps
// parsed past the cap are dropped: they stay only in the raw log, but their
// Seq numbers are still consumed so the gaps are visible on the server.
// Package-level so tests can shrink it.
var maxQueueBytes = 50 << 20 // 50 MiB

// maxQueueSteps bounds the number of steps held in the queue and in flight,
// independently of the byte budget: a stream of tiny steps must not create an
// unbounded slice. Package-level so tests can shrink it.
var maxQueueSteps = maxQueueBytes / stepOverheadBytes

// stepOverheadBytes is the conservative fixed cost of one queued step: the
// NewStep struct itself plus its slots in the steps/sizes slices (including
// slice growth), the string headers and the map header.
const stepOverheadBytes = 256

// payloadEntryBytes is the per-entry cost of a payload container: the map or
// slice element, the interface header and the allocator rounding. It is
// charged for keys and elements as well.
const payloadEntryBytes = 64

// payloadSlotBytes is the cost of one slot of a slice backing array. Every
// slot of the array's capacity is charged, including unfilled ones: a null
// element occupies 16 bytes of an []interface{} exactly like any other value.
const payloadSlotBytes = 16

// payloadScalarBytes is the cost of one non-string scalar payload value.
const payloadScalarBytes = 24

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

// drainGrace bounds how long Run waits for the stdout/stderr readers after
// the process tree is gone: a process that escaped the job object could still
// hold a pipe. Package-level so tests can shrink it.
var drainGrace = 5 * time.Second

// Run starts the agent process, pumps its stdout through the parser, mirrors
// both streams into the raw log and delivers step batches.
//
// The process exit and the end of the pipe streams are tracked separately:
// the parent can exit while a child it spawned still holds the inherited
// stdout/stderr handles, so waiting for EOF first would hang Run. Once the
// parent exits, the whole job is closed (killing such children) and only then
// is the rest of the log drained.
//
// Reading (stdout/stderr, plus parsing) and sending (RunSpec.Send) run on
// separate goroutines: a stuck network never stalls the agent on a full pipe.
//
// Run owns the stdio pipes (they are plain os.Pipe files, not exec's
// StdoutPipe/StderrPipe): cmd.Wait must never close them behind a reader's
// back.
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

	// Own every end of the stdio pipes. The child ends are closed in the
	// parent right after Start, otherwise our read ends would never see EOF.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return Result{}, fmt.Errorf("agents: stdin pipe: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		closeFiles(stdinR, stdinW)
		return Result{}, fmt.Errorf("agents: stdout pipe: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		closeFiles(stdinR, stdinW, stdoutR, stdoutW)
		return Result{}, fmt.Errorf("agents: stderr pipe: %w", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, stderrW

	group := procwin.NewGroup()
	group.Prepare(cmd)
	if err := cmd.Start(); err != nil {
		closeFiles(stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW)
		group.Close()
		return Result{}, fmt.Errorf("agents: start %s: %w", spec.Command, err)
	}
	// The child got its own duplicated handles: release ours so the read
	// ends observe EOF when every holder exits.
	closeFiles(stdinR, stdoutW, stderrW)
	if err := group.Add(cmd); err != nil {
		_ = cmd.Process.Kill()
		closeFiles(stdoutR, stderrR, stdinW)
		group.Close()
		return Result{}, fmt.Errorf("agents: add to group: %w", err)
	}

	// The prompt goes to stdin, then stdin is closed so the agent starts.
	go func() {
		_, _ = io.WriteString(stdinW, spec.Prompt)
		_ = stdinW.Close()
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
	// Send error both go through it.
	killReq := make(chan struct{})
	var killOnce sync.Once
	requestKill := func() {
		killOnce.Do(func() {
			group.Kill()
			close(killReq)
		})
	}
	var killTimeout atomic.Bool

	// The process exit is awaited on its own goroutine, independently of the
	// pipe readers: cmd.Wait must not be held back by a child that inherited
	// the stdio handles.
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	// Kill the whole tree when the context is done; finished closes when Run
	// is about to return so the watcher never lingers.
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			requestKill()
		case <-finished:
		}
	}()

	queue := newStepQueue()

	// The stdout reader owns the parser: it logs every line in full
	// (however long), feeds compacted lines to the parser and queues new
	// steps with their Seq numbers. Steps are drained from the parser after
	// every line, so neither the parser nor the raw line buffers accumulate.
	stdoutDone := make(chan struct{})
	go func() {
		br := bufio.NewReaderSize(stdoutR, 64<<10)
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
		closeFiles(stdoutR)
	}()

	stderrDone := make(chan struct{})
	go func() {
		br := bufio.NewReaderSize(stderrR, 64<<10)
		readLines(br, func(line []byte) {
			logStderr(string(line))
		})
		close(stderrDone)
		closeFiles(stderrR)
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

	// Wait for the parent process, not for EOF: a child may keep the pipes.
	var waitErr error
	select {
	case waitErr = <-waitCh:
		// The parent exited: reap what it left behind (a child may still hold
		// our stdio handles), so the readers reach EOF. Kill reaps the job on
		// Windows, Close releases the group handle on every platform.
		group.Kill()
		group.Close()
	case <-killReq:
		timer := time.NewTimer(killWaitTimeout)
		select {
		case waitErr = <-waitCh:
		case <-timer.C:
			// The tree ignored the kill: unblock the readers by closing the
			// read ends, and report the failure explicitly below.
			killTimeout.Store(true)
		}
		timer.Stop()
		group.Close()
		if killTimeout.Load() {
			closeFiles(stdoutR, stderrR)
		}
	}

	// Drain the rest of the log: the readers stop at EOF or when Run closes
	// the read ends (kill timeout / a process that escaped the job).
	drained := make(chan struct{})
	go func() {
		<-stdoutDone
		<-stderrDone
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(drainGrace):
		closeFiles(stdoutR, stderrR)
		<-drained
	}

	// Final flush: wait for the sender to drain the queue, unless ctx is
	// already done (then the process was killed; do not wait for the
	// network).
	flushed := false
	select {
	case <-senderDone:
		flushed = true
	case <-ctx.Done():
	}

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
	if killTimeout.Load() {
		return res, fmt.Errorf("agents: process tree did not exit within %s after kill", killWaitTimeout)
	}
	if flushed && sendErr != nil {
		return res, fmt.Errorf("agents: send steps: %w", sendErr)
	}
	return res, nil
}

// closeFiles closes the given files, ignoring errors and nil entries.
func closeFiles(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
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
// goroutine. Its budget covers the queued steps and the batch currently in
// flight together: bytes and the step count stay accounted for until Send
// returns.
type stepQueue struct {
	mu       sync.Mutex
	signal   chan struct{} // buffered 1: the batch is full or the queue closed
	steps    []NewStep
	sizes    []int
	bytes    int
	inFlight int // steps taken by take and not released yet
	closed   bool
	logged   bool // the overflow loss was already logged
}

func newStepQueue() *stepQueue {
	return &stepQueue{signal: make(chan struct{}, 1)}
}

// stepSize conservatively estimates the memory a queued step keeps alive: a
// fixed per-step cost (the struct, its slice slots, growth and headers), the
// summary bytes and a recursive payload estimate. The serialized JSON size is
// deliberately not used as the estimate: it misses the containers, their
// capacities and the keys, and under-counts tiny steps badly.
func stepSize(s NewStep) int {
	return stepOverheadBytes + len(s.Summary) + payloadSize(s.Payload)
}

// payloadSize estimates one payload value. The container header, the value of
// every entry and every slot of the backing array (charged for the whole
// capacity, so unfilled slots and nulls are not free) are accounted
// separately. Unknown container types are walked by reflection, so any JSON
// shape a parser keeps in the payload is covered.
func payloadSize(v interface{}) int {
	switch t := v.(type) {
	case nil:
		// A nil interface holds nothing of its own; the slot that contains it
		// is charged by the enclosing container.
		return 0
	case string:
		return len(t) + payloadEntryBytes
	case []string:
		if t == nil {
			return 0
		}
		size := payloadEntryBytes + cap(t)*payloadSlotBytes
		for _, e := range t {
			size += len(e) + payloadEntryBytes
		}
		return size
	case []interface{}:
		if t == nil {
			return 0
		}
		size := payloadEntryBytes + cap(t)*payloadSlotBytes
		for _, e := range t {
			size += payloadSize(e)
		}
		return size
	case []map[string]interface{}:
		if t == nil {
			return 0
		}
		size := payloadEntryBytes + cap(t)*payloadSlotBytes
		for _, e := range t {
			size += payloadSize(e)
		}
		return size
	case map[string]interface{}:
		if t == nil {
			return 0
		}
		size := payloadEntryBytes
		for k, e := range t {
			// The extra slot covers the map bucket and its overflow.
			size += len(k) + payloadEntryBytes + payloadSlotBytes + payloadSize(e)
		}
		return size
	default:
		return payloadContainerSize(v)
	}
}

// payloadContainerSize estimates a payload value of a type the fast path does
// not know: any other slice, array or map is walked by reflection (with the
// same slot and entry accounting), everything else is a scalar.
func payloadContainerSize(v interface{}) int {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return 0
		}
		size := payloadEntryBytes + rv.Cap()*payloadSlotBytes
		for i := 0; i < rv.Len(); i++ {
			size += payloadSize(rv.Index(i).Interface())
		}
		return size
	case reflect.Map:
		if rv.IsNil() {
			return 0
		}
		size := payloadEntryBytes
		iter := rv.MapRange()
		for iter.Next() {
			size += len(fmt.Sprint(iter.Key().Interface())) + payloadEntryBytes + payloadSlotBytes
			size += payloadSize(iter.Value().Interface())
		}
		return size
	default:
		return payloadScalarBytes + payloadEntryBytes
	}
}

// push appends a step unless the queue plus the in-flight batch is over the
// byte budget or the step limit; the loss is logged once to the runner's
// stderr (dropped steps stay in the raw log, their Seq numbers are still
// consumed).
func (q *stepQueue) push(s NewStep) {
	size := stepSize(s)
	q.mu.Lock()
	if q.bytes+size > maxQueueBytes || len(q.steps)+q.inFlight+1 > maxQueueSteps {
		if !q.logged {
			q.logged = true
			_, _ = fmt.Fprintf(os.Stderr,
				"agents: send queue over %d bytes or %d steps, dropping steps (they stay in the raw log; Seq gaps are expected)\n",
				maxQueueBytes, maxQueueSteps)
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
// whether the reader is done and no more steps will come. The budget stays
// accounted for until release, so the caller must release the batch.
func (q *stepQueue) take() (batch []NewStep, sizes []int, closed bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	batch, sizes = q.steps, q.sizes
	q.steps, q.sizes = nil, nil
	q.inFlight += len(batch)
	return batch, sizes, q.closed
}

// release frees the budget of a batch returned by take; it is called once
// Send has returned (successfully or not), when the batch is garbage.
func (q *stepQueue) release(sizes []int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, size := range sizes {
		q.bytes -= size
	}
	if q.bytes < 0 {
		q.bytes = 0
	}
	q.inFlight -= len(sizes)
	if q.inFlight < 0 {
		q.inFlight = 0
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
