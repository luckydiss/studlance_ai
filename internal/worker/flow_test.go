package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luckydiss/studlance_ai/internal/auth"
	blobfs "github.com/luckydiss/studlance_ai/internal/blobs/fs"
	"github.com/luckydiss/studlance_ai/internal/config"
	"github.com/luckydiss/studlance_ai/internal/httpapi"
	"github.com/luckydiss/studlance_ai/internal/id"
	"github.com/luckydiss/studlance_ai/internal/live"
	"github.com/luckydiss/studlance_ai/internal/queue"
	"github.com/luckydiss/studlance_ai/internal/store"
	"github.com/luckydiss/studlance_ai/internal/store/sqlite"
	"github.com/luckydiss/studlance_ai/internal/token"
	"github.com/luckydiss/studlance_ai/internal/worker/client"
)

// flowHarness runs the real server (httptest) plus a real Worker on fake
// agents: the order flow end to end inside the worker package. Works without
// pdftoppm (#no-pdf orders carry no previews).
type flowHarness struct {
	t       *testing.T
	srv     *httptest.Server
	token   string
	cookie  string
	workDir string
	logs    *bytes.Buffer
	cancel  context.CancelFunc
	done    chan error
}

func newFlowHarness(t *testing.T) *flowHarness {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	bl, err := blobfs.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("blobs: %v", err)
	}
	hub := live.New()
	q := queue.New(st, st.DB(), bl, hub, nil, queue.Options{ClaimWait: time.Second})
	authSvc := auth.New(st, auth.Config{SessionTTL: time.Hour})
	srv := httpapi.New(st, bl, authSvc, hub, q, config.DefaultServer(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv.Handler())

	secret := token.New()
	w := store.Worker{ID: id.New(), Name: "flow", TokenHash: token.Hash(secret), Capabilities: "[]", Info: "{}", CreatedAt: time.Now().UTC()}
	if err := st.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	hash, err := auth.HashPassword("pw")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	u := store.User{ID: id.New(), Email: "c@x.ru", PasswordHash: hash, Role: store.RoleClient, Name: "c", CreatedAt: time.Now().UTC()}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatalf("create user: %v", err)
	}

	h := &flowHarness{t: t, srv: ts, token: secret, workDir: t.TempDir(), logs: &bytes.Buffer{}}
	t.Cleanup(func() {
		h.stop()
		ts.Close()
		_ = st.Close()
	})

	// Login over HTTP.
	body, _ := json.Marshal(map[string]string{"email": "c@x.ru", "password": "pw"})
	resp, err := ts.Client().Post(ts.URL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie {
			h.cookie = c.Value
		}
	}
	if h.cookie == "" {
		t.Fatalf("no session cookie")
	}
	return h
}

func (h *flowHarness) start() {
	h.t.Helper()
	cfg := config.Worker{
		ServerURL: h.srv.URL,
		Token:     h.token,
		Name:      "flow",
		WorkDir:   h.workDir,
		PdfToppm:  "pdftoppm",
		Codex:     config.WorkerCommand{Command: fakeCodex},
		Claude:    config.WorkerCommand{Command: fakeClaude},
		Timeouts:  config.WorkerTimeouts{Stage: "30s", Heartbeat: "200ms"},
	}
	logger := slog.New(slog.NewTextHandler(h.logs, nil))
	w := New(cfg, client.New(cfg.ServerURL, cfg.Token), logger)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- w.Run(ctx) }()
}

func (h *flowHarness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
	}
	h.cancel = nil
}

func (h *flowHarness) call(method, path string, body io.Reader, out interface{}) int {
	h.t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: h.cookie})
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			h.t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

// newJob creates and submits an order with one input file.
func (h *flowHarness) newJob(prompt string) string {
	h.t.Helper()
	var d httpapi.ClientJobDetail
	raw, _ := json.Marshal(map[string]string{"prompt": prompt})
	if code := h.call(http.MethodPost, "/api/client/jobs", bytes.NewReader(raw), &d); code != http.StatusCreated {
		h.t.Fatalf("create: %d", code)
	}
	req, err := http.NewRequest(http.MethodPut, h.srv.URL+"/api/client/jobs/"+d.Id+"/input?path=%D0%B7%D0%B0%D0%B4%D0%B0%D0%BD%D0%B8%D0%B5.txt",
		strings.NewReader("Вариант 14."))
	if err != nil {
		h.t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: h.cookie})
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("upload: %d", resp.StatusCode)
	}
	if code := h.call(http.MethodPost, "/api/client/jobs/"+d.Id+"/submit", nil, &d); code != http.StatusOK {
		h.t.Fatalf("submit: %d", code)
	}
	return d.Id
}

func (h *flowHarness) waitStatus(jobID, want string) httpapi.ClientJobDetail {
	h.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var d httpapi.ClientJobDetail
		if code := h.call(http.MethodGet, "/api/client/jobs/"+jobID, nil, &d); code != http.StatusOK {
			h.t.Fatalf("get job: %d", code)
		}
		if string(d.ClientStatus) == want {
			return d
		}
		time.Sleep(150 * time.Millisecond)
	}
	h.t.Fatalf("timeout waiting for %s\nworker logs:\n%s", want, h.logs.String())
	return httpapi.ClientJobDetail{}
}

// Полный путь заказа: queued → draft → verify → done v1 (без превью).
func TestFlowDoneNoPDF(t *testing.T) {
	h := newFlowHarness(t)
	h.start()
	jobID := h.newJob("Сделай практическую работу по файлу #no-pdf")
	d := h.waitStatus(jobID, "done")
	if d.CurrentVersion != 1 {
		t.Fatalf("current_version = %d", d.CurrentVersion)
	}
	if len(d.Versions) != 1 || len(d.Versions[0].Documents) < 2 {
		t.Fatalf("versions = %+v", d.Versions)
	}
	// TASK.md и служебная папка на месте.
	if _, err := os.Stat(filepath.Join(h.workDir, jobID, "TASK.md")); err != nil {
		t.Fatalf("TASK.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.workDir, jobID, ".studlance", "state.json")); err != nil {
		t.Fatalf("state.json: %v", err)
	}
}

// Вопрос агента: needs_input → ответ клиента → action answer → done.
func TestFlowQuestion(t *testing.T) {
	h := newFlowHarness(t)
	h.start()
	jobID := h.newJob("Сделай практическую работу #ask #no-pdf")
	d := h.waitStatus(jobID, "needs_input")
	if d.Question == nil || *d.Question == "" {
		t.Fatalf("нет вопроса")
	}
	raw, _ := json.Marshal(map[string]string{"text": "Нагрузка 10 кН/м"})
	if code := h.call(http.MethodPost, "/api/client/jobs/"+jobID+"/answer", bytes.NewReader(raw), &d); code != http.StatusOK {
		t.Fatalf("answer: %d", code)
	}
	d = h.waitStatus(jobID, "done")
	if d.CurrentVersion != 1 {
		t.Fatalf("current_version = %d", d.CurrentVersion)
	}
	// Вопрос сохранён в .studlance/questions/q-1.md.
	if _, err := os.Stat(filepath.Join(h.workDir, jobID, ".studlance", "questions", "q-1.md")); err != nil {
		t.Fatalf("q-1.md: %v", err)
	}
}

// Отмена во время зависшего агента: canceled.
func TestFlowCancel(t *testing.T) {
	h := newFlowHarness(t)
	h.start()
	jobID := h.newJob("Сделай практическую работу #hang")
	h.waitStatus(jobID, "in_progress")
	if code := h.call(http.MethodPost, "/api/client/jobs/"+jobID+"/cancel", nil, nil); code != http.StatusOK {
		t.Fatalf("cancel: %d", code)
	}
	h.waitStatus(jobID, "canceled")
}
