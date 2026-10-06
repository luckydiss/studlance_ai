//go:build e2e

// Package e2e runs the real server + worker + fake agents end to end
// (09-tasks.md, PR 4). Requires pdftoppm in PATH (CI installs poppler-utils).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
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
	"github.com/luckydiss/studlance_ai/internal/worker"
	"github.com/luckydiss/studlance_ai/internal/worker/client"
)

var fakeagentCodex, fakeagentClaude string

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: pdftoppm not found in PATH, skipping (install poppler-utils)")
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "fakeagent-build")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	bin := filepath.Join(dir, "fakeagent")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", bin, "../cmd/fakeagent").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build fakeagent: %v\n%s\n", err, out)
		os.Exit(1)
	}
	// The worker config uses command + args, exactly like the real CLIs:
	// one binary copy named "codex", one named "claude" (mode from flags).
	raw, err := os.ReadFile(bin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	for _, name := range []string{"codex", "claude"} {
		p := filepath.Join(dir, name)
		if runtime.GOOS == "windows" {
			p += ".exe"
		}
		if err := os.WriteFile(p, raw, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "e2e:", err)
			os.Exit(1)
		}
		if name == "codex" {
			fakeagentCodex = p
		} else {
			fakeagentClaude = p
		}
	}
	os.Exit(m.Run())
}

// ---------- harness ----------

type harness struct {
	t            *testing.T
	st           *sqlite.Store
	srv          *httptest.Server
	hc           *http.Client
	workerToken  string
	clientCookie string
	adminCookie  string
	workDir      string
	logs         *bytes.Buffer

	// gate, when non-nil, can hold all /api/worker/* requests (network outage
	// simulation): gateBlock() blocks them until gateRelease is closed.
	gate        *atomic.Bool
	gateRelease chan struct{}

	workerCancel context.CancelFunc
	workerDone   chan error
}

func newHarness(t *testing.T) *harness {
	return newHarnessGate(t, false)
}

func newHarnessGate(t *testing.T, withGate bool) *harness {
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
	cfg := config.DefaultServer()
	hub := live.New()
	q := queue.New(st, st.DB(), bl, hub, nil, queue.Options{ClaimWait: 2 * time.Second})
	authSvc := auth.New(st, auth.Config{SessionTTL: time.Hour})
	srv := httpapi.New(st, bl, authSvc, hub, q, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := srv.Handler()
	h := &harness{t: t, st: st, workDir: t.TempDir(), logs: &bytes.Buffer{}}
	if withGate {
		h.gate = &atomic.Bool{}
		h.gateRelease = make(chan struct{})
		inner := handler
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h.gate.Load() && strings.HasPrefix(r.URL.Path, "/api/worker/") {
				<-h.gateRelease
			}
			inner.ServeHTTP(w, r)
		})
	}
	ts := httptest.NewServer(handler)
	h.srv = ts
	h.hc = ts.Client()
	t.Cleanup(func() {
		h.stopWorker()
		ts.Close()
		_ = st.Close()
	})

	// Worker token straight into the store.
	secret := token.New()
	w := store.Worker{ID: id.New(), Name: "e2e", TokenHash: token.Hash(secret), Capabilities: "[]", Info: "{}", CreatedAt: time.Now().UTC()}
	if err := st.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	h.workerToken = secret

	h.clientCookie = h.seedUser("client@example.com", "client-password", store.RoleClient)
	h.adminCookie = h.seedUser("admin@example.com", "admin-password", store.RoleAdmin)
	return h
}

func (h *harness) seedUser(email, password string, role store.Role) string {
	h.t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		h.t.Fatalf("hash: %v", err)
	}
	// Users are created through the store; login goes over HTTP like a browser.
	u := store.User{ID: id.New(), Email: email, PasswordHash: hash, Role: role, Name: email, CreatedAt: time.Now().UTC()}
	if err := h.st.CreateUser(context.Background(), u); err != nil {
		h.t.Fatalf("create user: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	resp := h.doRaw(http.MethodPost, "/api/auth/login", "", bytes.NewReader(body), "application/json")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("login %s: %d", email, resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie {
			return c.Value
		}
	}
	h.t.Fatalf("login %s: no session cookie", email)
	return ""
}

// startWorker launches the worker loop; restart with the same token simulates
// a worker reboot (same worker row, same work_dir).
func (h *harness) startWorker() {
	h.t.Helper()
	cfg := config.Worker{
		ServerURL: h.srv.URL,
		Token:     h.workerToken,
		Name:      "e2e",
		WorkDir:   h.workDir,
		PdfToppm:  "pdftoppm",
		Codex:     config.WorkerCommand{Command: fakeagentCodex},
		Claude:    config.WorkerCommand{Command: fakeagentClaude},
		Timeouts:  config.WorkerTimeouts{Stage: "1m", Heartbeat: "300ms"},
	}
	logger := slog.New(slog.NewTextHandler(h.logs, nil))
	w := worker.New(cfg, client.New(cfg.ServerURL, cfg.Token), logger)
	ctx, cancel := context.WithCancel(context.Background())
	h.workerCancel = cancel
	h.workerDone = make(chan error, 1)
	go func() { h.workerDone <- w.Run(ctx) }()
}

func (h *harness) stopWorker() {
	if h.workerCancel == nil {
		return
	}
	h.workerCancel()
	select {
	case <-h.workerDone:
	case <-time.After(15 * time.Second):
		h.t.Log("worker did not stop in 15 s")
	}
	h.workerCancel = nil
}

// ---------- HTTP helpers ----------

func (h *harness) doRaw(method, path, cookie string, body io.Reader, contentType string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+path, body)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: cookie})
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func (h *harness) doJSON(method, path, cookie string, in, out interface{}) int {
	h.t.Helper()
	var body io.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	}
	resp := h.doRaw(method, path, cookie, body, "application/json")
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			h.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

func (h *harness) clientJob(jobID string) httpapi.ClientJobDetail {
	h.t.Helper()
	var d httpapi.ClientJobDetail
	if code := h.doJSON(http.MethodGet, "/api/client/jobs/"+jobID, h.clientCookie, nil, &d); code != http.StatusOK {
		h.t.Fatalf("client job: %d", code)
	}
	return d
}

func (h *harness) adminJob(jobID string) httpapi.AdminJobDetail {
	h.t.Helper()
	var d httpapi.AdminJobDetail
	if code := h.doJSON(http.MethodGet, "/api/admin/jobs/"+jobID, h.adminCookie, nil, &d); code != http.StatusOK {
		h.t.Fatalf("admin job: %d", code)
	}
	return d
}

// newJob creates, uploads one input file and submits an order.
func (h *harness) newJob(prompt string) string {
	h.t.Helper()
	var d httpapi.ClientJobDetail
	if code := h.doJSON(http.MethodPost, "/api/client/jobs", h.clientCookie,
		map[string]string{"prompt": prompt}, &d); code != http.StatusCreated {
		h.t.Fatalf("create job: %d", code)
	}
	resp := h.doRaw(http.MethodPut, "/api/client/jobs/"+d.Id+"/input?path="+url.QueryEscape("задание.txt"),
		h.clientCookie, strings.NewReader("Практическая работа. Вариант 14. Исходные данные: q = 10 кН/м."), "application/octet-stream")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("upload input: %d", resp.StatusCode)
	}
	if code := h.doJSON(http.MethodPost, "/api/client/jobs/"+d.Id+"/submit", h.clientCookie, nil, &d); code != http.StatusOK {
		h.t.Fatalf("submit: %d", code)
	}
	return d.Id
}

// waitFor polls cond every 200 ms until timeout.
func (h *harness) waitFor(what string, timeout time.Duration, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	h.t.Fatalf("timeout waiting for %s\nworker logs:\n%s", what, h.logs.String())
}

func (h *harness) waitClientStatus(jobID, status string, timeout time.Duration) {
	h.t.Helper()
	want := httpapi.ClientJobDetailClientStatus(status)
	h.waitFor("client_status="+status, timeout, func() bool {
		return h.clientJob(jobID).ClientStatus == want
	})
}

// multipartRevision builds the revision multipart body (04-api.md).
func revisionBody(t *testing.T, data string, files map[string]string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("data", data); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		fw, err := mw.CreateFormFile("files[]", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

// poll is waitFor without failing.
func (h *harness) poll(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
