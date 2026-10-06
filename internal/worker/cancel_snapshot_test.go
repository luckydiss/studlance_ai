package worker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luckydiss/studlance_ai/internal/config"
	"github.com/luckydiss/studlance_ai/internal/httpapi"
	"github.com/luckydiss/studlance_ai/internal/worker/client"
)

// snapshotRecorder is a fake /api/worker server: it records snapshot uploads,
// commits and finishes, and can block the first snapshot PUT so a test can
// confirm a cancellation in the middle of the upload.
type snapshotRecorder struct {
	mu        sync.Mutex
	putFiles  int
	commits   int
	finishes  []string
	putStatus int  // non-204 status for snapshot file PUTs (e.g. 500)
	putStale  bool // snapshot file PUTs answer 409 stale_lease

	cancelHeartbeat atomic.Bool
	putStarted      chan struct{} // closed on the first snapshot file PUT
	putRelease      chan struct{} // test closes it to let the PUT finish
	putOnce         sync.Once
}

func newSnapshotRecorder() *snapshotRecorder {
	return &snapshotRecorder{
		putStarted: make(chan struct{}),
		putRelease: make(chan struct{}),
	}
}

func (r *snapshotRecorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		switch {
		case strings.HasSuffix(p, "/heartbeat"):
			cancel := r.cancelHeartbeat.Load()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"cancel": cancel})
		case strings.HasSuffix(p, "/commit"):
			r.mu.Lock()
			r.commits++
			r.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/finish"):
			var fr httpapi.FinishRequest
			_ = json.NewDecoder(req.Body).Decode(&fr)
			r.mu.Lock()
			r.finishes = append(r.finishes, string(fr.Outcome))
			r.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(p, "/snapshot/") && strings.HasSuffix(p, "/files"):
			r.mu.Lock()
			r.putFiles++
			status := r.putStatus
			stale := r.putStale
			r.mu.Unlock()
			r.putOnce.Do(func() { close(r.putStarted) })
			if stale {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":{"code":"stale_lease","message":"x"}}`))
				return
			}
			if status == 0 {
				<-r.putRelease
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// Statuses are returned after the release gate as well, so the
			// test controls the retry timing.
			<-r.putRelease
			w.WriteHeader(status)
		case strings.Contains(p, "/snapshot/"):
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func (r *snapshotRecorder) counters() (putFiles, commits int, finishes []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.putFiles, r.commits, append([]string(nil), r.finishes...)
}

// snapshotJob builds a jobExec with a manifest of the given document files and
// an empty .studlance tree. No document needs COM conversion unless the file
// extension says so.
func snapshotJob(t *testing.T, rec *snapshotRecorder, cfg config.Worker, docs ...string) (*jobExec, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(rec.handler())
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries := make([]map[string]string, 0, len(docs))
	for _, d := range docs {
		p := filepath.Join(dir, filepath.FromSlash(d))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("bytes of "+d), 0o644); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, map[string]string{"file": d, "title": d, "kind": "Документ"})
	}
	raw, _ := json.Marshal(map[string]any{"title": "t", "documents": entries})
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".studlance"), 0o755); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	j := &jobExec{
		w: &Worker{
			cfg:    cfg,
			cl:     client.New(srv.URL, "tok"),
			logger: logger,
			now:    func() time.Time { return time.Now().UTC() },
		},
		asn:         &httpapi.Assignment{JobId: "j1", Epoch: 1, Version: 2, Stage: httpapi.AssignmentStageRevise},
		dir:         dir,
		log:         logger,
		agentCancel: func() {},
	}
	return j, srv
}

// confirmCancelDuringPut arms the heartbeat to report a cancellation, waits
// until the worker knows about it, and only then releases the blocked PUT.
func confirmCancelDuringPut(t *testing.T, j *jobExec, rec *snapshotRecorder) {
	t.Helper()
	select {
	case <-rec.putStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("snapshot file PUT never started")
	}
	rec.cancelHeartbeat.Store(true)
	deadline := time.Now().Add(10 * time.Second)
	for !j.cancelRequested.Load() {
		if time.Now().After(deadline) {
			t.Fatal("heartbeat cancel was not observed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(rec.putRelease)
}

// Пункт 4 (второй раунд): отмена во время снимка. PUT snapshot/v1/files
// задержан, отмена подтверждена heartbeat'ом, затем PUT отпущен — воркер не
// должен делать commit, а обязан закончить canceled. Проверяется для draft и
// для v1/vN.
func TestCancelDuringSnapshotNoCommit(t *testing.T) {
	cases := []struct {
		name    string
		stage   string
		version int
		commit  func(ctx context.Context, j *jobExec) bool
	}{
		{"draft", "draft", 0, func(ctx context.Context, j *jobExec) bool { return j.commitDraft(ctx) }},
		{"v1", "verify", 1, func(ctx context.Context, j *jobExec) bool { return j.commitVersion(ctx, 1, "draft") }},
		{"vN", "revise", 2, func(ctx context.Context, j *jobExec) bool { return j.commitVersion(ctx, 2, "v1") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newSnapshotRecorder()
			cfg := config.Worker{Timeouts: config.WorkerTimeouts{Heartbeat: "20ms"}}
			j, _ := snapshotJob(t, rec, cfg, "out/чертёж.cdw")
			j.stage = tc.stage
			j.asn.Version = tc.version

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hbDone := make(chan struct{})
			go j.heartbeatLoop(ctx, hbDone)
			defer close(hbDone)

			done := make(chan bool, 1)
			go func() { done <- tc.commit(ctx, j) }()
			confirmCancelDuringPut(t, j, rec)

			select {
			case ok := <-done:
				if ok {
					t.Fatal("commit reported success after cancel")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("snapshot did not stop after cancel")
			}

			putFiles, commits, finishes := rec.counters()
			if commits != 0 {
				t.Fatalf("commits = %d, want 0", commits)
			}
			if len(finishes) != 1 || finishes[0] != string(httpapi.FinishRequestOutcomeCanceled) {
				t.Fatalf("finishes = %v, want exactly [canceled]", finishes)
			}
			if putFiles == 0 {
				t.Fatal("the delayed PUT never reached the server (test did not exercise the upload)")
			}
		})
	}
}

// Пункт 4 (второй раунд): повтор сетевого запроса не продолжается после
// подтверждённой отмены — второй попытки PUT нет, commit не делается.
func TestCancelStopsUploadRetry(t *testing.T) {
	old := uploadBackoffInitial
	uploadBackoffInitial = 10 * time.Millisecond
	t.Cleanup(func() { uploadBackoffInitial = old })

	rec := newSnapshotRecorder()
	rec.putStatus = http.StatusInternalServerError
	cfg := config.Worker{Timeouts: config.WorkerTimeouts{Heartbeat: "20ms"}}
	j, _ := snapshotJob(t, rec, cfg, "out/чертёж.cdw")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hbDone := make(chan struct{})
	go j.heartbeatLoop(ctx, hbDone)
	defer close(hbDone)

	done := make(chan bool, 1)
	go func() { done <- j.commitVersion(ctx, 1, "draft") }()
	confirmCancelDuringPut(t, j, rec)

	select {
	case ok := <-done:
		if ok {
			t.Fatal("commit reported success after cancel")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("snapshot did not stop after cancel")
	}
	putFiles, commits, finishes := rec.counters()
	if putFiles != 1 {
		t.Fatalf("PUT attempts = %d, want 1 (no retry after cancel)", putFiles)
	}
	if commits != 0 {
		t.Fatalf("commits = %d, want 0", commits)
	}
	if len(finishes) != 1 || finishes[0] != string(httpapi.FinishRequestOutcomeCanceled) {
		t.Fatalf("finishes = %v, want [canceled]", finishes)
	}
}

// Пункт 4 (второй раунд): ошибка конвертации/загрузки после отмены не
// приводит к commit: отмена уже известна, документ с превью не конвертируется,
// загрузки прекращаются, итог — canceled.
func TestCancelBeforeConversionAndUpload(t *testing.T) {
	rec := newSnapshotRecorder()
	rec.cancelHeartbeat.Store(true)
	cfg := config.Worker{Timeouts: config.WorkerTimeouts{Heartbeat: "20ms"}}
	// .docx would need an Office COM conversion if the cancel were ignored.
	j, _ := snapshotJob(t, rec, cfg, "out/доклад.docx")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hbDone := make(chan struct{})
	go j.heartbeatLoop(ctx, hbDone)
	defer close(hbDone)

	deadline := time.Now().Add(5 * time.Second)
	for !j.cancelRequested.Load() {
		if time.Now().After(deadline) {
			t.Fatal("heartbeat cancel was not observed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	start := time.Now()
	if j.commitVersion(ctx, 1, "draft") {
		t.Fatal("commit reported success after cancel")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("snapshot took %v: conversion/upload was not stopped", elapsed)
	}
	putFiles, commits, finishes := rec.counters()
	if putFiles != 0 {
		t.Fatalf("PUTs = %d, want 0 after a known cancel", putFiles)
	}
	if commits != 0 {
		t.Fatalf("commits = %d, want 0", commits)
	}
	if len(finishes) != 1 || finishes[0] != string(httpapi.FinishRequestOutcomeCanceled) {
		t.Fatalf("finishes = %v, want [canceled]", finishes)
	}
}

// Пункт 4 (второй раунд): stale_lease при снимке — остановка без finish.
func TestStaleLeaseDuringSnapshotNoFinish(t *testing.T) {
	rec := newSnapshotRecorder()
	rec.putStale = true
	cfg := config.Worker{Timeouts: config.WorkerTimeouts{Heartbeat: "20ms"}}
	j, _ := snapshotJob(t, rec, cfg, "out/чертёж.cdw")

	if j.commitVersion(context.Background(), 1, "draft") {
		t.Fatal("commit reported success on stale_lease")
	}
	_, commits, finishes := rec.counters()
	if commits != 0 {
		t.Fatalf("commits = %d, want 0", commits)
	}
	if len(finishes) != 0 {
		t.Fatalf("finishes = %v, want none on stale_lease", finishes)
	}
}
