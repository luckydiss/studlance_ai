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
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luckydiss/studlance_ai/internal/config"
	"github.com/luckydiss/studlance_ai/internal/httpapi"
	"github.com/luckydiss/studlance_ai/internal/worker/client"
)

// retryRecorder is a fake /api/worker server for the cancellation/retry tests.
// The first attempt of a path can fail with 500 (or a dropped connection) so
// the client's internal retry machinery is exercised; every attempt is
// recorded, including the finish outcomes.
type retryRecorder struct {
	srv *httptest.Server

	mu       sync.Mutex
	attempts map[string]int
	finishes []string

	putDrop int // drop this many first snapshot-file PUTs (broken connection)

	commitStarted chan struct{} // closed on the first commit attempt
	putStarted    chan struct{} // closed on the first snapshot-file PUT
	finishStarted chan struct{} // closed on the first finish-ok attempt

	cancelHeartbeat atomic.Bool
	staleHeartbeat  atomic.Bool
}

func newRetryRecorder(t *testing.T) *retryRecorder {
	t.Helper()
	rec := &retryRecorder{
		attempts:      map[string]int{},
		commitStarted: make(chan struct{}),
		putStarted:    make(chan struct{}),
		finishStarted: make(chan struct{}),
	}
	rec.srv = httptest.NewServer(rec.handler())
	t.Cleanup(rec.srv.Close)
	return rec
}

// waitClosed waits until ch is closed (the recorder saw the attempt).
func waitClosed(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(20 * time.Second):
		t.Fatalf("no %s", what)
	}
}

func (r *retryRecorder) bump(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts[key]++
	return r.attempts[key]
}

func (r *retryRecorder) attempt(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts[key]
}

func (r *retryRecorder) finishOutcomes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.finishes...)
}

func (r *retryRecorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		switch {
		case strings.HasSuffix(p, "/heartbeat"):
			if r.staleHeartbeat.Load() {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":{"code":"stale_lease","message":"x"}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"cancel": r.cancelHeartbeat.Load()})
		case strings.HasSuffix(p, "/commit"):
			// The first attempt fails, a retry would succeed: exactly what the
			// client's internal retry loop does.
			if r.bump("commit") == 1 {
				close(r.commitStarted)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/finish"):
			var fr httpapi.FinishRequest
			_ = json.NewDecoder(req.Body).Decode(&fr)
			outcome := string(fr.Outcome)
			r.mu.Lock()
			r.finishes = append(r.finishes, outcome)
			r.mu.Unlock()
			// The first ok attempt fails, its retry would succeed.
			if outcome == string(httpapi.FinishRequestOutcomeOk) {
				if r.bump("finish:ok") == 1 {
					close(r.finishStarted)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(p, "/snapshot/") && strings.HasSuffix(p, "/files"):
			if n := r.bump("put"); n == 1 {
				close(r.putStarted)
			}
			if n := r.attempt("put"); n <= r.putDrop {
				// A broken connection: the client sees a network error.
				if hj, ok := w.(http.Hijacker); ok {
					conn, _, err := hj.Hijack()
					if err == nil {
						_ = conn.Close()
						return
					}
				}
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

// retryJob builds a jobExec on the retry recorder with a manifest of the given
// documents (extensions without a converter keep the snapshot COM-free).
func retryJob(t *testing.T, rec *retryRecorder, cfg config.Worker, docs ...string) *jobExec {
	t.Helper()
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
	return &jobExec{
		w: &Worker{
			cfg:    cfg,
			cl:     client.New(rec.srv.URL, "tok"),
			logger: logger,
			now:    func() time.Time { return time.Now().UTC() },
		},
		asn:         &httpapi.Assignment{JobId: "j1", Epoch: 1, Version: 2},
		dir:         dir,
		log:         logger,
		agentCancel: func() {},
	}
}

// cancellableJob builds the job/service contexts the way run() does, so tests
// can exercise the real cancellation wiring.
func cancellableJob(t *testing.T, rec *retryRecorder, cfg config.Worker, heartbeat time.Duration, docs ...string) (*jobExec, context.Context) {
	t.Helper()
	j := retryJob(t, rec, cfg, docs...)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	workCtx, workCancel := context.WithCancel(ctx)
	t.Cleanup(workCancel)
	j.jobCtx, j.workCtx, j.workCancel = ctx, workCtx, workCancel

	// The client's retry backoff is the synchronisation point: it waits until
	// the worker knows about the cancellation (or the lost lease) before the
	// next attempt is even created.
	j.w.cl = client.NewForTest(rec.srv.URL, "tok", func(ctx context.Context, d time.Duration) error {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if j.cancelRequested.Load() || j.stale.Load() {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
		return nil
	})

	hbDone := make(chan struct{})
	go j.heartbeatLoop(ctx, hbDone)
	t.Cleanup(func() { close(hbDone) })
	// Give the heartbeat loop a moment to start before the test arms cancel.
	time.Sleep(2 * heartbeat)
	return j, ctx
}

// Пункт 4 (третий раунд): после подтверждённой отмены внутренние повторы
// HTTP-запросов прекращаются. Первый commit получает 500, до повтора приходит
// cancel — второго commit (который бы прошёл 204) быть не должно, а заказ
// завершается canceled. Проверяется для draft, v1 и vN.
func TestCancelStopsCommitRetry(t *testing.T) {
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
			const heartbeat = 20 * time.Millisecond
			rec := newRetryRecorder(t)
			cfg := config.Worker{Timeouts: config.WorkerTimeouts{Heartbeat: heartbeat.String()}}
			j, ctx := cancellableJob(t, rec, cfg, heartbeat, "out/чертёж.cdw")
			j.stage = tc.stage
			j.asn.Version = tc.version

			done := make(chan bool, 1)
			go func() { done <- tc.commit(j.activeCtx(ctx), j) }()
			// The first commit attempt is already in flight (it answers 500);
			// only now a cancellation is confirmed, before the client retries.
			waitClosed(t, rec.commitStarted, "first commit attempt")
			rec.cancelHeartbeat.Store(true)

			select {
			case ok := <-done:
				if ok {
					t.Fatal("commit reported success after the cancellation")
				}
			case <-time.After(20 * time.Second):
				t.Fatal("commit did not stop after the cancellation")
			}

			if got := rec.attempt("commit"); got != 1 {
				t.Fatalf("commit attempts = %d, want 1 (the 500 must not be retried after cancel)", got)
			}
			if got := rec.finishOutcomes(); !reflect.DeepEqual(got, []string{"canceled"}) {
				t.Fatalf("finishes = %v, want [canceled]", got)
			}
		})
	}
}

// Пункт 4 (третий раунд): finish ok не повторяется после отмены. Первый
// finish ok получает 500, до повтора приходит cancel — повтор ok не уходит,
// вместо него отправляется служебный finish canceled.
func TestCancelStopsFinishOkRetry(t *testing.T) {
	const heartbeat = 20 * time.Millisecond
	rec := newRetryRecorder(t)
	cfg := config.Worker{Timeouts: config.WorkerTimeouts{Heartbeat: heartbeat.String()}}
	j, ctx := cancellableJob(t, rec, cfg, heartbeat, "out/чертёж.cdw")

	v := 1
	done := make(chan struct{})
	go func() {
		j.finishOk(ctx, &v)
		close(done)
	}()
	// Arm the cancellation while the first finish-ok attempt is failing.
	waitClosed(t, rec.finishStarted, "first finish ok attempt")
	rec.cancelHeartbeat.Store(true)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("finishOk did not return")
	}

	if got := rec.attempt("finish:ok"); got != 1 {
		t.Fatalf("finish ok attempts = %d, want 1 (no retry after cancel)", got)
	}
	if got := rec.finishOutcomes(); !reflect.DeepEqual(got, []string{"ok", "canceled"}) {
		t.Fatalf("finishes = %v, want [ok canceled]", got)
	}
}

// Пункт 4 (третий раунд): stale lease во время ожидания повторной попытки —
// новых запросов нет и finish не вызывается вовсе.
func TestStaleLeaseStopsRetryWithoutFinish(t *testing.T) {
	const heartbeat = 20 * time.Millisecond
	rec := newRetryRecorder(t)
	cfg := config.Worker{Timeouts: config.WorkerTimeouts{Heartbeat: heartbeat.String()}}
	j, ctx := cancellableJob(t, rec, cfg, heartbeat, "out/чертёж.cdw")

	done := make(chan bool, 1)
	go func() { done <- j.commitVersion(j.activeCtx(ctx), 1, "draft") }()
	// The lease is lost while the first commit attempt is failing.
	waitClosed(t, rec.commitStarted, "first commit attempt")
	rec.staleHeartbeat.Store(true)
	select {
	case ok := <-done:
		if ok {
			t.Fatal("commit reported success after the lease was lost")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("commit did not stop after the lease was lost")
	}

	if got := rec.attempt("commit"); got != 1 {
		t.Fatalf("commit attempts = %d, want 1 (no retry after stale_lease)", got)
	}
	if got := rec.finishOutcomes(); len(got) != 0 {
		t.Fatalf("finishes = %v, want none on stale_lease", got)
	}
}

// Пункт 4 (третий раунд): обрыв соединения на загрузке снимка не
// превращается в повтор после отмены; commit не выполняется.
func TestCancelStopsUploadRetryOnNetworkError(t *testing.T) {
	old := uploadBackoffInitial
	uploadBackoffInitial = 100 * time.Millisecond
	t.Cleanup(func() { uploadBackoffInitial = old })

	const heartbeat = time.Millisecond
	rec := newRetryRecorder(t)
	rec.putDrop = 1
	cfg := config.Worker{Timeouts: config.WorkerTimeouts{Heartbeat: heartbeat.String()}}
	j, ctx := cancellableJob(t, rec, cfg, heartbeat, "out/чертёж.cdw")

	done := make(chan bool, 1)
	go func() { done <- j.commitVersion(j.activeCtx(ctx), 1, "draft") }()
	// The connection breaks on the first upload; the cancellation is
	// confirmed before the upload backoff expires.
	waitClosed(t, rec.putStarted, "first snapshot PUT")
	rec.cancelHeartbeat.Store(true)
	select {
	case ok := <-done:
		if ok {
			t.Fatal("commit reported success after cancel")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("snapshot did not stop after cancel")
	}
	if got := rec.attempt("put"); got != 1 {
		t.Fatalf("upload attempts = %d, want 1 (no retry after cancel)", got)
	}
	if got := rec.attempt("commit"); got != 0 {
		t.Fatalf("commit attempts = %d, want 0", got)
	}
	if got := rec.finishOutcomes(); !reflect.DeepEqual(got, []string{"canceled"}) {
		t.Fatalf("finishes = %v, want [canceled]", got)
	}
}
