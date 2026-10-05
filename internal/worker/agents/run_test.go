package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHelperProcess is not a test; it is the child process Run launches. It
// emits codex-shaped JSONL: thread.started, then HELPER_LINES agent_message
// items with HELPER_LINE_DELAY_MS between them, one stderr line, then sleeps
// HELPER_SLEEP_MS, writes HELPER_MARKER (proof it survived) and exits with
// HELPER_EXIT.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	n, _ := strconv.Atoi(os.Getenv("HELPER_LINES"))
	delayMs, _ := strconv.Atoi(os.Getenv("HELPER_LINE_DELAY_MS"))
	sleepMs, _ := strconv.Atoi(os.Getenv("HELPER_SLEEP_MS"))
	exitCode, _ := strconv.Atoi(os.Getenv("HELPER_EXIT"))
	marker := os.Getenv("HELPER_MARKER")

	fmt.Println(`{"type":"thread.started","thread_id":"helper-thread-1"}`)
	for i := 1; i <= n; i++ {
		fmt.Printf(`{"type":"item.completed","item":{"id":"item_%d","type":"agent_message","text":"helper step %d"}}`+"\n", i, i)
		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}
	}
	fmt.Fprintln(os.Stderr, "helper stderr line")
	if sleepMs > 0 {
		time.Sleep(time.Duration(sleepMs) * time.Millisecond)
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
	var session string
	spec := helperSpec(t, "HELPER_LINES=10", "HELPER_LINE_DELAY_MS=10", "HELPER_EXIT=0")
	spec.Send = col.send
	spec.OnSession = func(id string) { session = id }

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
	if session != "helper-thread-1" {
		t.Fatalf("OnSession id = %q, want %q", session, "helper-thread-1")
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
