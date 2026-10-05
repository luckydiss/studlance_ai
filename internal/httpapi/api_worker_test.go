package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"sync"
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
)

// ---------- harness ----------

// fakeClock is a manually advanced Clock for lease/timeout tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type wkHarness struct {
	t     *testing.T
	st    *sqlite.Store
	bl    *blobfs.Blobs
	clock *fakeClock
	queue *queue.Queue
	srv   *httptest.Server
	hc    *http.Client
}

func newWorkerHarness(t *testing.T) *wkHarness {
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
	cfg.MaxUpload = 1 << 20
	clock := newFakeClock()
	hub := live.New()
	q := queue.New(st, st.DB(), bl, hub, clock, queue.Options{
		ClaimWait: 2 * time.Second,
	})
	authSvc := auth.New(st, auth.Config{SessionTTL: time.Hour})
	srv := httpapi.New(st, bl, authSvc, hub, q, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv.Handler())
	h := &wkHarness{t: t, st: st, bl: bl, clock: clock, queue: q, srv: ts, hc: ts.Client()}
	t.Cleanup(func() {
		ts.Close()
		_ = st.Close()
	})
	return h
}

// seedWorker registers a worker row and returns its raw token.
func (h *wkHarness) seedWorker(name string) string {
	h.t.Helper()
	secret := token.New()
	w := store.Worker{
		ID: id.New(), Name: name, TokenHash: token.Hash(secret),
		Capabilities: "[]", Info: "{}", CreatedAt: time.Now().UTC(),
	}
	if err := h.st.CreateWorker(context.Background(), w); err != nil {
		h.t.Fatalf("create worker: %v", err)
	}
	return secret
}

// seedUserHTTP creates a user and logs in over HTTP, returning the cookie.
func (h *wkHarness) seedUserHTTP(email, password string, role store.Role) string {
	h.t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		h.t.Fatalf("hash: %v", err)
	}
	u := store.User{
		ID: id.New(), Email: email, PasswordHash: hash, Role: role,
		Name: email, CreatedAt: time.Now().UTC(),
	}
	if err := h.st.CreateUser(context.Background(), u); err != nil {
		h.t.Fatalf("create user: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	code, _, hdr := h.req(http.MethodPost, "/api/auth/login", nil, bytes.NewReader(body))
	if code != http.StatusOK {
		h.t.Fatalf("login: %d", code)
	}
	for _, c := range hdr["Set-Cookie"] {
		if strings.HasPrefix(c, auth.SessionCookie+"=") {
			return strings.Split(strings.TrimPrefix(c, auth.SessionCookie+"="), ";")[0]
		}
	}
	h.t.Fatalf("no session cookie")
	return ""
}

// req performs one HTTP request against the test server.
func (h *wkHarness) req(method, path string, hdr map[string]string, body io.Reader) (int, []byte, http.Header) {
	h.t.Helper()
	r, err := http.NewRequest(method, h.srv.URL+path, body)
	if err != nil {
		h.t.Fatalf("request: %v", err)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := h.hc.Do(r)
	if err != nil {
		h.t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, b, resp.Header
}

// wreq performs a worker API request with the Bearer token.
func (h *wkHarness) wreq(method, path, wtoken string, body io.Reader) (int, []byte) {
	h.t.Helper()
	code, b, _ := h.req(method, path, map[string]string{"Authorization": "Bearer " + wtoken}, body)
	return code, b
}

// creq performs a client/admin API request with the session cookie.
func (h *wkHarness) creq(method, path, cookie string, body io.Reader) (int, []byte) {
	h.t.Helper()
	hdr := map[string]string{"Cookie": auth.SessionCookie + "=" + cookie}
	code, b, _ := h.req(method, path, hdr, body)
	return code, b
}

// creqCT performs a client request with an explicit Content-Type.
func (h *wkHarness) creqCT(method, path, cookie, contentType string, body io.Reader) (int, []byte) {
	h.t.Helper()
	hdr := map[string]string{"Cookie": auth.SessionCookie + "=" + cookie, "Content-Type": contentType}
	code, b, _ := h.req(method, path, hdr, body)
	return code, b
}

func jsonReader(v interface{}) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

func decodeAs[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode: %v body=%s", err, b)
	}
	return v
}

// ---------- shared JSON shapes ----------

type tAssignment struct {
	JobID          string                 `json:"job_id"`
	Epoch          int                    `json:"epoch"`
	Action         string                 `json:"action"`
	Stage          string                 `json:"stage"`
	Attempt        int                    `json:"attempt"`
	Version        int                    `json:"version"`
	Prompt         string                 `json:"prompt"`
	LeaseExpiresAt string                 `json:"lease_expires_at"`
	Answer         *string                `json:"answer"`
	State          map[string]interface{} `json:"state"`
	Revision       *struct {
		Version int    `json:"version"`
		Comment string `json:"comment"`
		Remarks []struct {
			Idx           int     `json:"idx"`
			DocumentTitle string  `json:"document_title"`
			FilePath      string  `json:"file_path"`
			Page          int     `json:"page"`
			Text          string  `json:"text"`
			X             float64 `json:"x"`
		} `json:"remarks"`
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	} `json:"revision"`
}

type tClientJob struct {
	ID             string  `json:"id"`
	ClientStatus   string  `json:"client_status"`
	StatusText     string  `json:"status_text"`
	CurrentVersion int     `json:"current_version"`
	Question       *string `json:"question"`
	StatusSteps    []struct {
		Title string `json:"title"`
		State string `json:"state"`
	} `json:"status_steps"`
	Versions []struct {
		Version   int `json:"version"`
		Documents []struct {
			ID          string `json:"id"`
			FilePath    string `json:"file_path"`
			PageCount   int    `json:"page_count"`
			DownloadURL string `json:"download_url"`
		} `json:"documents"`
	} `json:"versions"`
}

type tAdminJob struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	Stage          *string `json:"stage"`
	Attempt        int     `json:"attempt"`
	NeedsAttention bool    `json:"needs_attention"`
	CurrentVersion int     `json:"current_version"`
	Error          *string `json:"error"`
	Draft          *struct {
		Documents []struct {
			ID          string `json:"id"`
			FilePath    string `json:"file_path"`
			DownloadURL string `json:"download_url"`
		} `json:"documents"`
	} `json:"draft"`
	Verification *struct {
		Found int `json:"found"`
		Fixed int `json:"fixed"`
	} `json:"verification"`
	AgentRuns []struct {
		ID      string  `json:"id"`
		Outcome *string `json:"outcome"`
	} `json:"agent_runs"`
}

// ---------- flow helpers ----------

// submitJob creates a job with one input file and submits it.
func (h *wkHarness) submitJob(cookie, prompt string) string {
	h.t.Helper()
	code, b := h.creq(http.MethodPost, "/api/client/jobs", cookie, jsonReader(map[string]string{"prompt": prompt}))
	if code != http.StatusCreated {
		h.t.Fatalf("create job: %d %s", code, b)
	}
	job := decodeAs[tClientJob](h.t, b)
	code, b = h.creq(http.MethodPut, "/api/client/jobs/"+job.ID+"/input?path=задание.txt", cookie, strings.NewReader("TASK CONTENT"))
	if code != http.StatusOK {
		h.t.Fatalf("upload: %d %s", code, b)
	}
	code, b = h.creq(http.MethodPost, "/api/client/jobs/"+job.ID+"/submit", cookie, nil)
	if code != http.StatusOK {
		h.t.Fatalf("submit: %d %s", code, b)
	}
	return job.ID
}

// claim expects a 200 assignment.
func (h *wkHarness) claim(wtoken string) tAssignment {
	h.t.Helper()
	code, b := h.wreq(http.MethodPost, "/api/worker/claim", wtoken, nil)
	if code != http.StatusOK {
		h.t.Fatalf("claim: %d %s", code, b)
	}
	return decodeAs[tAssignment](h.t, b)
}

// claimExpect204 expects no work.
func (h *wkHarness) claimExpect204(wtoken string) {
	h.t.Helper()
	code, b := h.wreq(http.MethodPost, "/api/worker/claim", wtoken, nil)
	if code != http.StatusNoContent {
		h.t.Fatalf("claim: %d want 204 %s", code, b)
	}
}

// uploadSnapshot uploads one out/ file (+preview), pages and thumbs for a
// single-document snapshot.
func (h *wkHarness) uploadSnapshot(wtoken, jobID string, epoch int, snap, content string) {
	h.t.Helper()
	ep := fmt.Sprintf("epoch=%d", epoch)
	code, b := h.wreq(http.MethodPut, "/api/worker/jobs/"+jobID+"/snapshot/"+snap+"/files?"+ep+"&path=записка.docx", wtoken, strings.NewReader(content))
	if code != http.StatusNoContent {
		h.t.Fatalf("upload snapshot file: %d %s", code, b)
	}
	code, b = h.wreq(http.MethodPut, "/api/worker/jobs/"+jobID+"/snapshot/"+snap+"/files?"+ep+"&path="+urlQueryEscape("preview/записка.pdf"), wtoken, strings.NewReader("PDF"))
	if code != http.StatusNoContent {
		h.t.Fatalf("upload preview: %d %s", code, b)
	}
	code, b = h.wreq(http.MethodPut, "/api/worker/jobs/"+jobID+"/snapshot/"+snap+"/pages/0/1?"+ep, wtoken, strings.NewReader("PNG-PAGE"))
	if code != http.StatusNoContent {
		h.t.Fatalf("upload page: %d %s", code, b)
	}
	code, b = h.wreq(http.MethodPut, "/api/worker/jobs/"+jobID+"/snapshot/"+snap+"/thumbs/0/1?"+ep, wtoken, strings.NewReader("PNG-THUMB"))
	if code != http.StatusNoContent {
		h.t.Fatalf("upload thumb: %d %s", code, b)
	}
}

// commitSnapshot commits the single-document snapshot.
func (h *wkHarness) commitSnapshot(wtoken, jobID string, epoch int, snap, title string) {
	h.t.Helper()
	body := map[string]interface{}{
		"epoch": epoch,
		"title": title,
		"documents": []map[string]interface{}{{
			"idx": 0, "title": "Пояснительная записка", "kind": "Word",
			"file_path": "записка.docx", "preview_path": "preview/записка.pdf",
			"page_count": 1,
			"pages": []map[string]interface{}{{
				"page": 1, "width": 100, "height": 140,
				"changed_boxes": []map[string]interface{}{{"x": 0.1, "y": 0.2, "w": 0.3, "h": 0.05}},
			}},
		}},
	}
	if snap != "draft" {
		body["verification"] = map[string]interface{}{
			"found": 2, "fixed": 2,
			"remaining": []map[string]interface{}{},
		}
	}
	code, b := h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/snapshot/"+snap+"/commit", wtoken, jsonReader(body))
	if code != http.StatusNoContent {
		h.t.Fatalf("commit %s: %d %s", snap, code, b)
	}
}

// finish expects a 204 finish.
func (h *wkHarness) finish(wtoken, jobID string, epoch int, outcome string, version *int, errText *string) {
	h.t.Helper()
	body := map[string]interface{}{"epoch": epoch, "outcome": outcome}
	if version != nil {
		body["version"] = *version
	}
	if errText != nil {
		body["error"] = *errText
	}
	code, b := h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/finish", wtoken, jsonReader(body))
	if code != http.StatusNoContent {
		h.t.Fatalf("finish %s: %d %s", outcome, code, b)
	}
}

func (h *wkHarness) clientJob(cookie, jobID string) tClientJob {
	h.t.Helper()
	code, b := h.creq(http.MethodGet, "/api/client/jobs/"+jobID, cookie, nil)
	if code != http.StatusOK {
		h.t.Fatalf("client job: %d %s", code, b)
	}
	return decodeAs[tClientJob](h.t, b)
}

func (h *wkHarness) adminJob(cookie, jobID string) tAdminJob {
	h.t.Helper()
	code, b := h.creq(http.MethodGet, "/api/admin/jobs/"+jobID, cookie, nil)
	if code != http.StatusOK {
		h.t.Fatalf("admin job: %d %s", code, b)
	}
	return decodeAs[tAdminJob](h.t, b)
}

// workToDone drives a job from claim to done v1 (with runs, steps, snapshots).
func (h *wkHarness) workToDone(wtoken, jobID string) tAssignment {
	h.t.Helper()
	asn := h.claim(wtoken)
	if asn.Action != "start" || asn.Stage != "draft" || asn.Attempt != 0 {
		h.t.Fatalf("claim: %+v", asn)
	}
	h.createRunWithSteps(wtoken, jobID, asn, "codex")
	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "draft", "DRAFT DOC")
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "draft", "Готовая записка")
	h.createRunWithSteps(wtoken, jobID, asn, "claude")
	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "v1", "FINAL DOC")
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "v1", "")
	v1 := 1
	h.finish(wtoken, jobID, asn.Epoch, "ok", &v1, nil)
	return asn
}

// createRunWithSteps creates a run, appends steps, patches it and uploads a log.
func (h *wkHarness) createRunWithSteps(wtoken, jobID string, asn tAssignment, agent string) string {
	h.t.Helper()
	code, b := h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/runs", wtoken, jsonReader(map[string]interface{}{
		"epoch": asn.Epoch, "agent": agent, "stage": asn.Stage, "version": asn.Version, "attempt": asn.Attempt,
	}))
	if code != http.StatusCreated {
		h.t.Fatalf("create run: %d %s", code, b)
	}
	runID := decodeAs[map[string]string](h.t, b)["run_id"]
	code, b = h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/runs/"+runID+"/steps", wtoken, jsonReader(map[string]interface{}{
		"epoch": asn.Epoch,
		"steps": []map[string]interface{}{
			{"seq": 1, "ts": time.Now().UTC(), "type": "message", "summary": "приступил"},
			{"seq": 2, "ts": time.Now().UTC(), "type": "command", "summary": "команда", "payload": map[string]interface{}{"command": "ls"}},
		},
	}))
	if code != http.StatusNoContent {
		h.t.Fatalf("steps: %d %s", code, b)
	}
	code, b = h.wreq(http.MethodPut, "/api/worker/jobs/"+jobID+"/runs/"+runID+"/log?epoch="+itoa(int64(asn.Epoch)), wtoken, strings.NewReader("{\"line\":1}\n"))
	if code != http.StatusNoContent {
		h.t.Fatalf("log: %d %s", code, b)
	}
	outcome := "ok"
	code, b = h.wreq(http.MethodPatch, "/api/worker/jobs/"+jobID+"/runs/"+runID, wtoken, jsonReader(map[string]interface{}{
		"epoch": asn.Epoch, "outcome": outcome, "input_tokens": 10, "output_tokens": 5, "cost_usd": 0.01,
	}))
	if code != http.StatusNoContent {
		h.t.Fatalf("patch run: %d %s", code, b)
	}
	return runID
}

func urlQueryEscape(s string) string {
	r := strings.NewReplacer("/", "%2F", " ", "%20")
	return r.Replace(s)
}

func strPtr(s string) *string { return &s }

// multipartBody builds a revision multipart request.
func multipartBody(t *testing.T, data map[string]interface{}, files map[string]string) (io.Reader, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	raw, _ := json.Marshal(data)
	if err := w.WriteField("data", string(raw)); err != nil {
		t.Fatalf("field: %v", err)
	}
	for name, content := range files {
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename="%s"`, name))
		hdr.Set("Content-Type", "application/octet-stream")
		pw, err := w.CreatePart(hdr)
		if err != nil {
			t.Fatalf("part: %v", err)
		}
		_, _ = pw.Write([]byte(content))
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf, w.FormDataContentType()
}

var _ = bufio.NewReader
