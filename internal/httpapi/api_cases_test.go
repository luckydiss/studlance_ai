package httpapi_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckydiss/studlance_ai/internal/auth"
	"github.com/luckydiss/studlance_ai/internal/store"
)

func TestLoginLogoutWrongPassword(t *testing.T) {
	h := newHarness(t)
	h.seedUser("client@local", "correct", store.RoleClient)

	// wrong password
	body, _ := json.Marshal(map[string]string{"email": "client@local", "password": "nope"})
	rec := h.do(http.MethodPost, "/api/auth/login", bytes.NewReader(body), nil, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status %d", rec.Code)
	}

	// correct password
	cookie := h.login("client@local", "correct")

	// me
	rec = h.do(http.MethodGet, "/api/auth/me", nil, cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("me: status %d", rec.Code)
	}
	me := decode[map[string]map[string]string](t, rec)
	if me["user"]["email"] != "client@local" {
		t.Fatalf("me user: %v", me)
	}

	// logout
	rec = h.do(http.MethodPost, "/api/auth/logout", nil, cookie, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: status %d", rec.Code)
	}
	rec = h.do(http.MethodGet, "/api/auth/me", nil, cookie, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout: status %d", rec.Code)
	}
}

func TestLoginRateLimited(t *testing.T) {
	h := newHarness(t)
	h.seedUser("client@local", "correct", store.RoleClient)

	body, _ := json.Marshal(map[string]string{"email": "client@local", "password": "nope"})
	for i := 0; i < auth.MaxLoginAttempts; i++ {
		rec := h.do(http.MethodPost, "/api/auth/login", bytes.NewReader(body), nil, map[string]string{"Content-Type": "application/json"})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i, rec.Code)
		}
	}
	// 11th attempt is rate limited (even with correct password)
	bodyOK, _ := json.Marshal(map[string]string{"email": "client@local", "password": "correct"})
	rec := h.do(http.MethodPost, "/api/auth/login", bytes.NewReader(bodyOK), nil, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limit: status %d body %s", rec.Code, rec.Body.String())
	}
}

// TestCrossUserNotFound verifies a client cannot see another client's job on
// any client endpoint (404, indistinguishable from missing).
func TestCrossUserNotFound(t *testing.T) {
	h := newHarness(t)
	h.seedUser("a@local", "pw", store.RoleClient)
	h.seedUser("b@local", "pw", store.RoleClient)
	cookieA := h.login("a@local", "pw")

	// A creates a job.
	rec := h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": "Сделай курсовую работу по теме"}), cookieA, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body.String())
	}
	job := decode[map[string]interface{}](t, rec)
	jobID := job["id"].(string)

	// B tries to access A's job.
	cookieB := h.login("b@local", "pw")
	paths := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/client/jobs/" + jobID},
		{http.MethodPost, "/api/client/jobs/" + jobID + "/submit"},
		{http.MethodPost, "/api/client/jobs/" + jobID + "/cancel"},
		{http.MethodPost, "/api/client/jobs/" + jobID + "/answer"},
		{http.MethodDelete, "/api/client/jobs/" + jobID + "/input?path=x.txt"},
		{http.MethodGet, "/api/client/jobs/" + jobID + "/versions/1/files/x.docx"},
		{http.MethodGet, "/api/client/jobs/" + jobID + "/versions/1/bundle.zip"},
		{http.MethodGet, "/api/client/jobs/" + jobID + "/versions/1/documents/doc/pages"},
	}
	for _, p := range paths {
		var body io.Reader
		if p.method == http.MethodPost && strings.HasSuffix(p.path, "/answer") {
			body = bytes.NewReader([]byte(`{"text":"hi"}`))
		}
		req := httptest.NewRequest(p.method, p.path, body)
		req.AddCookie(cookieB)
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: status %d want 404", p.method, p.path, rec.Code)
		}
	}
}

func TestForbiddenTransitions(t *testing.T) {
	h := newHarness(t)
	h.seedUser("c@local", "pw", store.RoleClient)
	cookie := h.login("c@local", "pw")

	rec := h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": "Запрос на двадцать символов точно есть"}), cookie, map[string]string{"Content-Type": "application/json"})
	job := decode[map[string]interface{}](t, rec)
	jobID := job["id"].(string)

	// answer while uploading -> 409
	rec = h.do(http.MethodPost, "/api/client/jobs/"+jobID+"/answer", jsonBody(map[string]string{"text": "ответ"}), cookie, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("answer in uploading: status %d want 409", rec.Code)
	}
	// revise while uploading -> 409
	body, ct := multipartRevision(map[string]interface{}{"comment": "x", "remarks": []interface{}{}}, nil)
	rec = h.do(http.MethodPost, "/api/client/jobs/"+jobID+"/revisions", body, cookie, map[string]string{"Content-Type": ct})
	if rec.Code != http.StatusConflict {
		t.Fatalf("revise in uploading: status %d want 409", rec.Code)
	}
	// submit
	rec = h.do(http.MethodPost, "/api/client/jobs/"+jobID+"/submit", nil, cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit: status %d body %s", rec.Code, rec.Body.String())
	}
	// submit again -> 409
	rec = h.do(http.MethodPost, "/api/client/jobs/"+jobID+"/submit", nil, cookie, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("double submit: status %d want 409", rec.Code)
	}
	// answer while queued -> 409
	rec = h.do(http.MethodPost, "/api/client/jobs/"+jobID+"/answer", jsonBody(map[string]string{"text": "ответ"}), cookie, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("answer in queued: status %d want 409", rec.Code)
	}
}

func TestUploadPathsAndRejectDotDot(t *testing.T) {
	h := newHarness(t)
	h.seedUser("c@local", "pw", store.RoleClient)
	cookie := h.login("c@local", "pw")

	rec := h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": "Файлы с путями папок и подпапок"}), cookie, map[string]string{"Content-Type": "application/json"})
	job := decode[map[string]interface{}](t, rec)
	jobID := job["id"].(string)

	// valid nested path with backslashes normalizes to forward slashes
	rec = h.do(http.MethodPut, "/api/client/jobs/"+jobID+"/input?path="+urlq("методичка/примеры/задание.pdf"), strings.NewReader("PDFDATA"), cookie, map[string]string{"Content-Type": "application/octet-stream"})
	if rec.Code != http.StatusOK {
		t.Fatalf("upload nested: status %d body %s", rec.Code, rec.Body.String())
	}
	var up struct {
		Path string `json:"path"`
		Size int    `json:"size"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &up)
	if up.Path != "методичка/примеры/задание.pdf" {
		t.Fatalf("path %q", up.Path)
	}

	// reject ".."
	rec = h.do(http.MethodPut, "/api/client/jobs/"+jobID+"/input?path="+urlq("../escape.txt"), strings.NewReader("x"), cookie, map[string]string{"Content-Type": "application/octet-stream"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("dotdot: status %d want 400", rec.Code)
	}

	// detail shows the input file
	rec = h.do(http.MethodGet, "/api/client/jobs/"+jobID, nil, cookie, nil)
	detail := decode[struct {
		InputFiles []struct {
			Path string `json:"path"`
			Size int    `json:"size"`
		} `json:"input_files"`
	}](t, rec)
	if len(detail.InputFiles) != 1 || detail.InputFiles[0].Path != "методичка/примеры/задание.pdf" {
		t.Fatalf("input_files: %+v", detail.InputFiles)
	}
}

func urlq(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == '/':
			b.WriteByte('%')
			b.WriteString("2F")
		default:
			for _, by := range []byte(string(r)) {
				b.WriteByte('%')
				const hex = "0123456789ABCDEF"
				b.WriteByte(hex[by>>4])
				b.WriteByte(hex[by&0x0f])
			}
		}
	}
	return b.String()
}

func TestBundleZipRussianNames(t *testing.T) {
	h := newHarness(t)
	h.seedUser("c@local", "pw", store.RoleClient)
	cookie := h.login("c@local", "pw")

	// Create a job and simulate a ready version by writing blobs + documents.
	rec := h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": "Заказ с готовой версией для архива"}), cookie, map[string]string{"Content-Type": "application/json"})
	job := decode[map[string]interface{}](t, rec)
	jobID := job["id"].(string)
	_ = h.do(http.MethodPost, "/api/client/jobs/"+jobID+"/submit", nil, cookie, nil)

	// Write an out/ file with a Russian name.
	if _, _, err := h.blobs.Put(testCtx(), "jobs/"+jobID+"/v1/out/Пояснительная записка.docx", strings.NewReader("DOCX")); err != nil {
		t.Fatalf("put blob: %v", err)
	}
	// Mark the job done with version 1 directly in the store.
	j, _ := h.store.JobByID(testCtx(), jobID)
	j.Status = store.StatusDone
	j.CurrentVersion = 1
	now := time.Now().UTC()
	j.FinishedAt = &now
	if err := h.store.UpdateJob(testCtx(), j); err != nil {
		t.Fatalf("update job: %v", err)
	}

	rec = h.do(http.MethodGet, "/api/client/jobs/"+jobID+"/versions/1/bundle.zip", nil, cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bundle: status %d body %s", rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	found := false
	for _, f := range zr.File {
		if f.Name == "Пояснительная записка.docx" {
			found = true
			if !f.NonUTF8 && f.Flags&0x800 == 0 {
				t.Fatalf("EFS flag not set for %q (flags=%x)", f.Name, f.Flags)
			}
		}
	}
	if !found {
		t.Fatalf("russian filename not found: %v", zr.File)
	}
}

func TestSSESnapshotAndJob(t *testing.T) {
	h := newHarness(t)
	h.seedUser("c@local", "pw", store.RoleClient)
	cookie := h.login("c@local", "pw")

	rec := h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": "SSE тест заказа на поток событий"}), cookie, map[string]string{"Content-Type": "application/json"})
	job := decode[map[string]interface{}](t, rec)
	jobID := job["id"].(string)

	// Use a cancellable request so the stream ends.
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/client/jobs/"+jobID+"/stream", nil).WithContext(ctx)
	req.AddCookie(cookie)
	rec2 := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		h.handler.ServeHTTP(rec2, req)
		close(done)
	}()

	// Wait for the snapshot event.
	if !waitFor(rec2, "event: snapshot", 2*time.Second) {
		cancel()
		<-done
		t.Fatalf("no snapshot event; body=%s", rec2.bodyString())
	}
	// Trigger a job event by submitting.
	h.do(http.MethodPost, "/api/client/jobs/"+jobID+"/submit", nil, cookie, nil)
	if !waitFor(rec2, "event: job", 2*time.Second) {
		cancel()
		<-done
		t.Fatalf("no job event; body=%s", rec2.bodyString())
	}
	cancel()
	<-done
}

func TestVersionFileAndPageRead(t *testing.T) {
	h := newHarness(t)
	h.seedUser("c@local", "pw", store.RoleClient)
	cookie := h.login("c@local", "pw")

	rec := h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": "Заказ с файлами версии и страницами"}), cookie, map[string]string{"Content-Type": "application/json"})
	job := decode[map[string]interface{}](t, rec)
	jobID := job["id"].(string)
	_ = h.do(http.MethodPost, "/api/client/jobs/"+jobID+"/submit", nil, cookie, nil)

	// Write a nested out/ file and a page image.
	if _, _, err := h.blobs.Put(testCtx(), "jobs/"+jobID+"/v1/out/папка/файл.txt", strings.NewReader("CONTENT")); err != nil {
		t.Fatalf("put file: %v", err)
	}
	docID := newID()
	if _, _, err := h.blobs.Put(testCtx(), "jobs/"+jobID+"/v1/pages/0/1.png", strings.NewReader("PNGDATA")); err != nil {
		t.Fatalf("put page: %v", err)
	}
	if _, _, err := h.blobs.Put(testCtx(), "jobs/"+jobID+"/v1/thumbs/0/1.png", strings.NewReader("THUMB")); err != nil {
		t.Fatalf("put thumb: %v", err)
	}
	// Insert a document + page in the store and mark the job done.
	if err := h.store.ReplaceDocuments(testCtx(), jobID, store.SnapshotVersion, 1, []store.DocumentWithPages{{
		Document: store.Document{ID: docID, JobID: jobID, Snapshot: store.SnapshotVersion, Version: 1, Idx: 0, Title: "Записка", Kind: "Word", FilePath: "папка/файл.txt", PageCount: 1},
		Pages:    []store.Page{{DocumentID: docID, Page: 1, ImageKey: "jobs/" + jobID + "/v1/pages/0/1.png", ThumbKey: "jobs/" + jobID + "/v1/thumbs/0/1.png", Width: 100, Height: 140, ChangedBoxes: "[]"}},
	}}); err != nil {
		t.Fatalf("replace docs: %v", err)
	}
	j, _ := h.store.JobByID(testCtx(), jobID)
	j.Status = store.StatusDone
	j.CurrentVersion = 1
	now := time.Now().UTC()
	j.FinishedAt = &now
	if err := h.store.UpdateJob(testCtx(), j); err != nil {
		t.Fatalf("update job: %v", err)
	}

	// nested file download via percent-encoded single segment
	rec = h.do(http.MethodGet, "/api/client/jobs/"+jobID+"/versions/1/files/"+urlq("папка/файл.txt"), nil, cookie, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "CONTENT" {
		t.Fatalf("file: status %d body %q", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "filename*=UTF-8''") {
		t.Fatalf("content-disposition %q", cd)
	}

	// page with .png suffix
	rec = h.do(http.MethodGet, "/api/client/jobs/"+jobID+"/versions/1/documents/"+docID+"/pages", nil, cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pages: status %d body %s", rec.Code, rec.Body.String())
	}
	var pl struct {
		Pages []struct {
			Page     int    `json:"page"`
			ImageUrl string `json:"image_url"`
		} `json:"pages"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &pl)
	if len(pl.Pages) != 1 || pl.Pages[0].ImageUrl == "" {
		t.Fatalf("pages body: %s", rec.Body.String())
	}
	rec = h.do(http.MethodGet, "/api/client/jobs/"+jobID+"/versions/1/pages/"+docID+"/1.png", nil, cookie, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "PNGDATA" {
		t.Fatalf("page: status %d body %q", rec.Code, rec.Body.String())
	}
}

func testCtx() context.Context { return context.Background() }

type flushRecorder struct {
	h    http.Header
	code int
	buf  *bytes.Buffer
	mu   chan struct{}
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{h: http.Header{}, buf: &bytes.Buffer{}, mu: make(chan struct{}, 1)}
}

func (f *flushRecorder) Header() http.Header         { return f.h }
func (f *flushRecorder) WriteHeader(code int)        { f.code = code }
func (f *flushRecorder) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *flushRecorder) Flush()                      {}
func (f *flushRecorder) bodyString() string          { return f.buf.String() }

func waitFor(f *flushRecorder, needle string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(f.buf.String(), needle) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return strings.Contains(f.buf.String(), needle)
}
