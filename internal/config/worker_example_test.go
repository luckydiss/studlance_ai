package config

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWorkerExampleLoadsWithProductionLoader(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not return the test source path")
	}
	example := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "worker.example.toml"))
	got, err := LoadWorker(example)
	if err != nil {
		t.Fatalf("LoadWorker(worker.example.toml): %v", err)
	}
	if got.ServerURL != "http://127.0.0.1:8080" || got.Name != "pc-1" {
		t.Fatalf("unexpected server/name: %q / %q", got.ServerURL, got.Name)
	}
	if got.Token != "" {
		t.Fatal("public example must not contain a worker token")
	}
	if !strings.HasSuffix(got.WorkDir, `C:\studlance\jobs`) {
		t.Fatalf("work_dir was not loaded as the configured persistent path: %q", got.WorkDir)
	}
	if got.PdfToppm != `C:\studlance\poppler\bin\pdftoppm.exe` {
		t.Fatalf("unexpected pdftoppm path: %q", got.PdfToppm)
	}
	if got.Codex.Command != "codex" || got.Claude.Command != "claude" {
		t.Fatalf("unexpected CLI commands: codex=%q claude=%q", got.Codex.Command, got.Claude.Command)
	}
	if got.StageDuration() != 3*time.Hour || got.HeartbeatDuration() != 15*time.Second {
		t.Fatalf("unexpected timeouts: stage=%s heartbeat=%s", got.StageDuration(), got.HeartbeatDuration())
	}
}
