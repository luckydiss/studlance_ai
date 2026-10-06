package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// heapAlloc returns the live heap after a collection.
func heapAlloc() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// TestHelperProcess is not a test; it is the child process Run launches. It
// emits codex-shaped JSONL and understands two modes (HELPER_MODE):
//
//   - "" (default): thread.started, then HELPER_LINES agent_message items
//     with HELPER_LINE_DELAY_MS between them (text is "helper step N", or
//     HELPER_LINE_BYTES of 'x' when set), one stderr line, then sleeps
//     HELPER_SLEEP_MS, then HELPER_TAIL_LINES more items, writes
//     HELPER_MARKER (proof it survived) and exits with HELPER_EXIT.
//   - "longline": thread.started, one command_execution item whose
//     aggregated_output is HELPER_LONG_BYTES of 'x' (default 10 MiB), then
//     two agent_message items, exit HELPER_EXIT.
//   - "bigoutput": thread.started, HELPER_LINES command_execution items with
//     HELPER_LINE_BYTES (default 900 KiB) of 'x' in aggregated_output each,
//     then HELPER_MARKER, then HELPER_TAIL_LINES small agent_message items,
//     exit HELPER_EXIT.
//   - "parentexit": parent starts a grandchild that inherits stdout/stderr
//     (HELPER_GRANDCHILD_SLEEP_MS, default 120 s), records its pid in
//     HELPER_GRANDCHILD_PID_FILE, writes two agent_message items and exits
//     while the grandchild is still alive. HELPER_GRANDCHILD=1 makes the
//     re-executed helper the sleeping grandchild itself.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	exitCode, _ := strconv.Atoi(os.Getenv("HELPER_EXIT"))

	if os.Getenv("HELPER_MODE") == "parentexit" {
		if os.Getenv("HELPER_GRANDCHILD") == "1" {
			sleepMs, _ := strconv.Atoi(os.Getenv("HELPER_GRANDCHILD_SLEEP_MS"))
			if sleepMs <= 0 {
				sleepMs = 120000
			}
			time.Sleep(time.Duration(sleepMs) * time.Millisecond)
			os.Exit(0)
		}
		fmt.Println(`{"type":"thread.started","thread_id":"helper-thread-parent"}`)
		child := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "--")
		child.Env = append(os.Environ(), "HELPER_GRANDCHILD=1")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "grandchild start:", err)
			os.Exit(2)
		}
		fmt.Println(`{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"parent before exit"}}`)
		if pidFile := os.Getenv("HELPER_GRANDCHILD_PID_FILE"); pidFile != "" {
			_ = os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o644)
		}
		if marker := os.Getenv("HELPER_MARKER"); marker != "" {
			_ = os.WriteFile(marker, []byte("done"), 0o644)
		}
		fmt.Println(`{"type":"item.completed","item":{"id":"m2","type":"agent_message","text":"parent last line"}}`)
		os.Exit(exitCode)
	}

	if os.Getenv("HELPER_MODE") == "bigoutput" {
		n, _ := strconv.Atoi(os.Getenv("HELPER_LINES"))
		if n <= 0 {
			n = 40
		}
		lineBytes, _ := strconv.Atoi(os.Getenv("HELPER_LINE_BYTES"))
		if lineBytes <= 0 {
			lineBytes = 900 * 1024
		}
		tail, _ := strconv.Atoi(os.Getenv("HELPER_TAIL_LINES"))
		fmt.Println(`{"type":"thread.started","thread_id":"helper-thread-big"}`)
		blob := strings.Repeat("x", lineBytes)
		for i := 1; i <= n; i++ {
			fmt.Printf(`{"type":"item.completed","item":{"id":"cmd_%d","type":"command_execution","command":"cmd %d","aggregated_output":%q,"exit_code":0}}`+"\n", i, i, blob)
		}
		if marker := os.Getenv("HELPER_MARKER"); marker != "" {
			_ = os.WriteFile(marker, []byte("done"), 0o644)
		}
		for i := 1; i <= tail; i++ {
			fmt.Printf(`{"type":"item.completed","item":{"id":"tail_%d","type":"agent_message","text":"after big %d"}}`+"\n", i, i)
		}
		os.Exit(exitCode)
	}

	if os.Getenv("HELPER_MODE") == "longline" {
		n, _ := strconv.Atoi(os.Getenv("HELPER_LONG_BYTES"))
		if n <= 0 {
			n = 10 << 20
		}
		fmt.Println(`{"type":"thread.started","thread_id":"helper-thread-long"}`)
		fmt.Printf(`{"type":"item.completed","item":{"id":"big1","type":"command_execution","command":"big-output","aggregated_output":"%s","exit_code":0}}`+"\n",
			strings.Repeat("x", n))
		fmt.Println(`{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"after long line 1"}}`)
		fmt.Println(`{"type":"item.completed","item":{"id":"m2","type":"agent_message","text":"after long line 2"}}`)
		os.Exit(exitCode)
	}

	n, _ := strconv.Atoi(os.Getenv("HELPER_LINES"))
	delayMs, _ := strconv.Atoi(os.Getenv("HELPER_LINE_DELAY_MS"))
	sleepMs, _ := strconv.Atoi(os.Getenv("HELPER_SLEEP_MS"))
	tail, _ := strconv.Atoi(os.Getenv("HELPER_TAIL_LINES"))
	lineBytes, _ := strconv.Atoi(os.Getenv("HELPER_LINE_BYTES"))
	marker := os.Getenv("HELPER_MARKER")

	line := func(i int) {
		text := fmt.Sprintf("helper step %d", i)
		if lineBytes > 0 {
			text = strings.Repeat("x", lineBytes)
		}
		fmt.Printf(`{"type":"item.completed","item":{"id":"item_%d","type":"agent_message","text":%q}}`+"\n", i, text)
	}

	fmt.Println(`{"type":"thread.started","thread_id":"helper-thread-1"}`)
	for i := 1; i <= n; i++ {
		line(i)
		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}
	}
	fmt.Fprintln(os.Stderr, "helper stderr line")
	if sleepMs > 0 {
		time.Sleep(time.Duration(sleepMs) * time.Millisecond)
	}
	for i := n + 1; i <= n+tail; i++ {
		line(i)
	}
	if marker != "" {
		_ = os.WriteFile(marker, []byte("done"), 0o644)
	}
	os.Exit(exitCode)
}

// collector records Send batches and fails on the errAt-th call (1-based).
type collector struct {
	mu      sync.Mutex
	batches [][]NewStep
	calls   int
	errAt   int
}

func (c *collector) send(steps []NewStep) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.errAt > 0 && c.calls == c.errAt {
		return errors.New("send failed (test)")
	}
	c.batches = append(c.batches, append([]NewStep(nil), steps...))
	return nil
}

func (c *collector) all() []NewStep {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []NewStep
	for _, b := range c.batches {
		out = append(out, b...)
	}
	return out
}

// fastFlush shrinks the batching knobs for tests.
func fastFlush(t *testing.T, interval time.Duration, batch int) {
	t.Helper()
	oldI, oldB := flushInterval, flushBatchSize
	flushInterval, flushBatchSize = interval, batch
	t.Cleanup(func() { flushInterval, flushBatchSize = oldI, oldB })
}

// helperSpec builds a RunSpec for the helper process.
func helperSpec(t *testing.T, env ...string) RunSpec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	// os.Executable does not propagate to the child via RunSpec; the env
	// must be injected through RunSpec.Command environment — Run uses
	// exec.Command, which inherits os.Environ, so set the helper env on the
	// test process for the duration of the run.
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	return RunSpec{
		Agent:   "codex",
		Command: exe,
		Args:    []string{"-test.run=^TestHelperProcess$"},
		Dir:     t.TempDir(),
		Prompt:  "test prompt",
		LogPath: filepath.Join(t.TempDir(), "run.jsonl"),
	}
}

func TestRunCollectsSteps(t *testing.T) {
	fastFlush(t, 30*time.Millisecond, 4)
	var col collector
	sessionCh := make(chan string, 1)
	spec := helperSpec(t, "HELPER_LINES=10", "HELPER_LINE_DELAY_MS=10", "HELPER_EXIT=0")
	spec.Send = col.send
	spec.OnSession = func(id string) { sessionCh <- id }

	res, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.Killed {
		t.Fatal("Killed = true, want false")
	}
	if res.AgentFailed != "" {
		t.Fatalf("AgentFailed = %q, want empty", res.AgentFailed)
	}
	select {
	case id := <-sessionCh:
		if id != "helper-thread-1" {
			t.Fatalf("OnSession id = %q, want %q", id, "helper-thread-1")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnSession was not called")
	}

	steps := col.all()
	if len(steps) != 10 {
		t.Fatalf("sent steps = %d, want 10", len(steps))
	}
	for i, s := range steps {
		if s.Seq != i+1 {
			t.Fatalf("step %d Seq = %d, want %d", i, s.Seq, i+1)
		}
		if s.Type != "message" {
			t.Fatalf("step %d Type = %q, want message", i, s.Type)
		}
		if want := fmt.Sprintf("helper step %d", i+1); s.Summary != want {
			t.Fatalf("step %d Summary = %q, want %q", i, s.Summary, want)
		}
	}

	log, err := os.ReadFile(spec.LogPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(log), `"helper step 10"`) {
		t.Fatal("log does not contain stdout lines")
	}
	if !strings.Contains(string(log), `{"_stderr":"helper stderr line"}`) {
		t.Fatal("log does not contain the stderr line as JSON")
	}
}

// TestRunLongLine: a 10 MiB stdout line is logged in full and still parsed
// (compacted first); the run does not hang and later lines keep coming.
func TestRunLongLine(t *testing.T) {
	fastFlush(t, 50*time.Millisecond, 50)
	var col collector
	spec := helperSpec(t, "HELPER_MODE=longline", "HELPER_EXIT=0")
	spec.Send = col.send

	res, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}

	// The raw log holds the long line in full.
	info, err := os.Stat(spec.LogPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if info.Size() < 10<<20 {
		t.Fatalf("log size = %d, want at least the full 10 MiB line", info.Size())
	}

	steps := col.all()
	wantTypes := []string{"command", "message", "message"}
	if len(steps) != len(wantTypes) {
		t.Fatalf("sent steps = %d, want %d", len(steps), len(wantTypes))
	}
	for i, want := range wantTypes {
		if steps[i].Type != want {
			t.Fatalf("step %d Type = %q, want %q", i, steps[i].Type, want)
		}
		if steps[i].Seq != i+1 {
			t.Fatalf("step %d Seq = %d, want %d", i, steps[i].Seq, i+1)
		}
	}

	cmd := steps[0]
	out, _ := cmd.Payload["output"].(string)
	if len(out) != 20*1024 {
		t.Fatalf("output bytes = %d, want %d (tail of the compacted output)", len(out), 20*1024)
	}
	if out != strings.Repeat("x", 20*1024) {
		t.Fatal("output is not the tail of aggregated_output")
	}
	note, _ := cmd.Payload["_truncated"].(string)
	if !strings.Contains(note, "обрезано") {
		t.Fatalf("payload _truncated = %q, want a cut note", note)
	}
}

// TestRunSlowNetworkDoesNotBlockReader: a Send blocked for 3 s on the first
// batch must not stall the reader — the helper process still runs to exit
// and every step arrives afterwards with a monotone Seq.
func TestRunSlowNetworkDoesNotBlockReader(t *testing.T) {
	fastFlush(t, 50*time.Millisecond, 50)
	marker := filepath.Join(t.TempDir(), "finished")
	var col collector
	var once sync.Once
	send := func(steps []NewStep) error {
		once.Do(func() {
			time.Sleep(3 * time.Second)
			// The process could exit only if the reader kept draining its
			// stdout while this Send was blocked.
			if _, err := os.Stat(marker); err != nil {
				t.Errorf("helper did not exit while Send was blocked: %v", err)
			}
		})
		return col.send(steps)
	}
	spec := helperSpec(t, "HELPER_LINES=10", "HELPER_EXIT=0", "HELPER_MARKER="+marker)
	spec.Send = send

	start := time.Now()
	res, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 3*time.Second {
		t.Fatalf("Run took %v, want at least the blocked first Send", elapsed)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}

	steps := col.all()
	if len(steps) != 10 {
		t.Fatalf("sent steps = %d, want 10", len(steps))
	}
	for i, s := range steps {
		if s.Seq != i+1 {
			t.Fatalf("step %d Seq = %d, want %d", i, s.Seq, i+1)
		}
	}
}

// TestRunQueueOverflowDropsSteps: with the queue memory cap shrunk to 10 KB
// and Send blocked, most of a 100-step burst is dropped (raw log only), Seq
// numbers are still consumed, and the tail steps resume at Seq 101.
func TestRunQueueOverflowDropsSteps(t *testing.T) {
	fastFlush(t, 50*time.Millisecond, 50)
	oldMax := maxQueueBytes
	maxQueueBytes = 10 * 1024
	t.Cleanup(func() { maxQueueBytes = oldMax })

	release := make(chan struct{})
	var col collector
	var once sync.Once
	send := func(steps []NewStep) error {
		once.Do(func() { <-release }) // the first batch blocks until released
		return col.send(steps)
	}
	spec := helperSpec(t,
		"HELPER_LINES=100", "HELPER_LINE_BYTES=1024",
		"HELPER_SLEEP_MS=1500", "HELPER_TAIL_LINES=3", "HELPER_EXIT=0")
	spec.Send = send

	go func() {
		time.Sleep(1 * time.Second)
		close(release)
	}()

	res, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}

	steps := col.all()
	if len(steps) == 0 {
		t.Fatal("no steps arrived at all")
	}
	if len(steps) >= 103 {
		t.Fatalf("sent steps = %d, want some dropped (100 burst + 3 tail)", len(steps))
	}
	for i := 1; i < len(steps); i++ {
		if steps[i].Seq <= steps[i-1].Seq {
			t.Fatalf("Seq not increasing at %d: %d after %d", i, steps[i].Seq, steps[i-1].Seq)
		}
	}
	tailSteps := steps[len(steps)-3:]
	for i, want := range []int{101, 102, 103} {
		if tailSteps[i].Seq != want {
			t.Fatalf("tail step %d Seq = %d, want %d (dropped steps must consume Seq)", i, tailSteps[i].Seq, want)
		}
	}
}

func TestRunExitCode(t *testing.T) {
	fastFlush(t, 20*time.Millisecond, 2)
	spec := helperSpec(t, "HELPER_LINES=2", "HELPER_EXIT=3")
	res, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}
	if res.Killed {
		t.Fatal("Killed = true, want false")
	}
}

func TestRunCancelKills(t *testing.T) {
	fastFlush(t, 20*time.Millisecond, 5)
	marker := filepath.Join(t.TempDir(), "finished")
	spec := helperSpec(t,
		"HELPER_LINES=2", "HELPER_SLEEP_MS=10000", "HELPER_EXIT=0", "HELPER_MARKER="+marker)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	res, err := Run(ctx, spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("Run took %v; the killed process should not be awaited", elapsed)
	}
	if !res.Killed {
		t.Fatal("Killed = false, want true")
	}
	if res.KillTimeout {
		t.Fatal("KillTimeout = true, want false (the job object kill is prompt)")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("marker file exists: the process was not killed")
	}
}

func TestRunSendErrorKills(t *testing.T) {
	fastFlush(t, 50*time.Millisecond, 3)
	marker := filepath.Join(t.TempDir(), "finished")
	col := &collector{errAt: 3}
	spec := helperSpec(t,
		"HELPER_LINES=12", "HELPER_LINE_DELAY_MS=30", "HELPER_SLEEP_MS=5000",
		"HELPER_EXIT=0", "HELPER_MARKER="+marker)
	spec.Send = col.send

	res, err := Run(context.Background(), spec)
	if err == nil {
		t.Fatal("Run error = nil, want the send error")
	}
	if !strings.Contains(err.Error(), "send failed (test)") {
		t.Fatalf("Run error = %v, want the send error", err)
	}
	if res.Killed {
		t.Fatal("Killed = true without ctx cancel, want false")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("marker file exists: the process was not killed after the send error")
	}
}

// Пункт 2 (второй раунд): лимит памяти общий для очереди и отправляемой
// пачки — пока Send не вернулся, байты пачки всё ещё заняты.
func TestStepQueueCountsInFlightBatch(t *testing.T) {
	old := maxQueueBytes
	maxQueueBytes = 3 * 1024
	t.Cleanup(func() { maxQueueBytes = old })

	q := newStepQueue()
	step := func(seq int) NewStep {
		return NewStep{Seq: seq, Type: "message", Summary: strings.Repeat("x", 1024)}
	}
	for i := 1; i <= 3; i++ {
		q.push(step(i))
	}
	batch, sizes, closed := q.take()
	if len(batch) != 3 || closed {
		t.Fatalf("take = %d steps, closed=%v; want 3, false", len(batch), closed)
	}
	// The batch is in flight (Send has not returned yet): the cap covers it.
	q.push(step(4))
	if more, _, _ := q.take(); len(more) != 0 {
		t.Fatalf("in-flight batch is not counted: %d more steps were queued", len(more))
	}
	// After Send returns the bytes are released and the queue accepts again.
	q.release(sizes)
	q.push(step(4))
	more, _, _ := q.take()
	if len(more) != 1 || more[0].Seq != 4 {
		t.Fatalf("after release take = %+v, want the one new step", more)
	}
}

// Пункт 2 (второй раунд): хвост payload — короткая копия, а не подстрока,
// удерживающая весь исходный вывод. 40 шагов по 900 КиБ aggregated_output
// не должны удерживать ~36 МиБ.
func TestParserTailCopyKeepsHeapBounded(t *testing.T) {
	big := strings.Repeat("x", 900*1024)
	line := []byte(`{"type":"item.completed","item":{"id":"c","type":"command_execution","command":"c","aggregated_output":"` + big + `","exit_code":0}}`)

	p := NewCodexParser()
	var keep []Step
	// Warm-up so the first parse's one-off allocations do not skew the delta.
	p.Feed(line, time.Now())
	keep = append(keep, p.Drain()...)
	before := heapAlloc()

	const n = 40
	for i := 0; i < n; i++ {
		p.Feed(line, time.Now())
		keep = append(keep, p.Drain()...)
	}
	after := heapAlloc()
	if len(keep) != n+1 {
		t.Fatalf("kept steps = %d, want %d", len(keep), n+1)
	}
	if out, _ := keep[n].Payload["output"].(string); len(out) != outputTailBytes {
		t.Fatalf("payload output = %d bytes, want %d", len(out), outputTailBytes)
	}
	res := int64(after) - int64(before)
	if res > 15<<20 {
		t.Fatalf("drained steps retain %d bytes of heap, want < 15 MiB (tails must be copies)", res)
	}
	if got := p.Drain(); len(got) != 0 {
		t.Fatalf("Drain returned %d steps twice", len(got))
	}
}

// Пункт 2 (второй раунд): длинный поток больших событий при недоступной сети
// 30 с — чтение не блокируется, сырой лог полный, память не растёт со всем
// историческим трейсом, после восстановления принятые шаги доходят.
func TestRunNetworkDownBigTrace(t *testing.T) {
	fastFlush(t, 50*time.Millisecond, 20)
	oldMax := maxQueueBytes
	maxQueueBytes = 8 << 20
	t.Cleanup(func() { maxQueueBytes = oldMax })

	const (
		lines     = 40
		lineBytes = 900 * 1024
		tailLines = 3
	)
	marker := filepath.Join(t.TempDir(), "produced")
	spec := helperSpec(t,
		"HELPER_MODE=bigoutput",
		"HELPER_LINES="+strconv.Itoa(lines),
		"HELPER_LINE_BYTES="+strconv.Itoa(lineBytes),
		"HELPER_TAIL_LINES="+strconv.Itoa(tailLines),
		"HELPER_MARKER="+marker,
		"HELPER_EXIT=0",
	)

	before := heapAlloc()
	var during uint64
	var sendErr error
	var firstCall sync.Once
	var mu sync.Mutex
	var seqs []int
	spec.Send = func(steps []NewStep) error {
		firstCall.Do(func() {
			// Network is down: Send blocks, the reader must keep draining.
			time.Sleep(30 * time.Second)
			if _, err := os.Stat(marker); err != nil {
				sendErr = fmt.Errorf("helper did not finish while Send was blocked: %w", err)
			}
			during = heapAlloc()
		})
		mu.Lock()
		for _, s := range steps {
			seqs = append(seqs, s.Seq)
		}
		mu.Unlock()
		return nil
	}

	start := time.Now()
	res, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Second {
		t.Fatalf("Run took %v, want at least the 30 s outage", elapsed)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}

	// The raw log keeps every line, including the last big event.
	info, err := os.Stat(spec.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < int64(lines*lineBytes) {
		t.Fatalf("raw log = %d bytes, want >= %d", info.Size(), lines*lineBytes)
	}
	raw, err := os.ReadFile(spec.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"cmd `+strconv.Itoa(lines)+`"`) {
		t.Fatalf("raw log is missing the last big event")
	}
	raw = nil

	// Memory during the outage stays bounded: the whole historical trace
	// (40 x 900 KiB) must not be retained.
	if delta := int64(during) - int64(before); delta > 20<<20 {
		t.Fatalf("heap during the outage grew by %d bytes, want < 20 MiB", delta)
	}

	// After recovery every accepted step arrives, in Seq order, up to the tail.
	mu.Lock()
	got := append([]int(nil), seqs...)
	mu.Unlock()
	if len(got) == 0 {
		t.Fatal("no steps arrived after recovery")
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("Seq not increasing at %d: %d after %d", i, got[i], got[i-1])
		}
	}
	wantLast := lines + tailLines
	if got[len(got)-1] != wantLast {
		t.Fatalf("last Seq = %d, want %d (tail steps must arrive)", got[len(got)-1], wantLast)
	}
}
