//go:build windows

package agents

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// processAlive reports whether pid still exists.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// Пункт 3 (второй раунд): обычный выход родителя (код 0) при живом ребёнке,
// который держит унаследованные stdout/stderr. Раньше Run ждал EOF обоих
// потоков до cmd.Wait, поэтому висел, пока ребёнок не умрёт. Теперь выход
// процесса отслеживается отдельно: Job закрывается сразу после выхода
// родителя, остаток лога дочитывается.
func TestRunParentExitWithChildPipes(t *testing.T) {
	fastFlush(t, 20*time.Millisecond, 2)
	dir := t.TempDir()
	marker := filepath.Join(dir, "parent-done")
	pidFile := filepath.Join(dir, "grandchild.pid")

	var col collector
	spec := helperSpec(t,
		"HELPER_MODE=parentexit",
		"HELPER_GRANDCHILD_SLEEP_MS=120000",
		"HELPER_MARKER="+marker,
		"HELPER_GRANDCHILD_PID_FILE="+pidFile,
		"HELPER_EXIT=0",
	)
	spec.Send = col.send

	start := time.Now()
	res, err := Run(context.Background(), spec)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("Run took %v; a normal parent exit must not wait for the child (120 s)", elapsed)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.Killed {
		t.Fatal("Killed = true, want false")
	}
	if res.KillTimeout {
		t.Fatal("KillTimeout = true, want false")
	}

	// The log is not truncated: it holds the last line the parent wrote.
	raw, err := os.ReadFile(spec.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "parent last line") {
		t.Fatalf("raw log is truncated:\n%s", raw)
	}

	// The child that held the pipes is dead: closing the Job reaped it.
	pidRaw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("grandchild pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil || pid <= 0 {
		t.Fatalf("grandchild pid = %q", pidRaw)
	}
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived the parent exit", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}

	steps := col.all()
	if len(steps) != 2 {
		t.Fatalf("sent steps = %d, want 2", len(steps))
	}
	for i, s := range steps {
		if s.Seq != i+1 || s.Type != "message" {
			t.Fatalf("step %d = %+v", i, s)
		}
	}
}
