package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/luckydiss/studlance_ai/internal/auth"
	blobfs "github.com/luckydiss/studlance_ai/internal/blobs/fs"
	"github.com/luckydiss/studlance_ai/internal/config"
	"github.com/luckydiss/studlance_ai/internal/httpapi"
	"github.com/luckydiss/studlance_ai/internal/live"
	"github.com/luckydiss/studlance_ai/internal/store"
	"github.com/luckydiss/studlance_ai/internal/store/sqlite"
)

type harness struct {
	t       *testing.T
	store   *sqlite.Store
	blobs   *blobfs.Blobs
	auth    *auth.Service
	handler http.Handler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	bs, err := blobfs.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("blobs: %v", err)
	}
	cfg := config.DefaultServer()
	cfg.MaxUpload = 1 << 20
	authSvc := auth.New(st, auth.Config{SessionTTL: time.Hour})
	hub := live.New()
	srv := httpapi.New(st, bs, authSvc, hub, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return &harness{t: t, store: st, blobs: bs, auth: authSvc, handler: srv.Handler()}
}

// seedUser creates a user with the given role and password.
func (h *harness) seedUser(email, password string, role store.Role) store.User {
	h.t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		h.t.Fatalf("hash: %v", err)
	}
	u := store.User{
		ID: newID(), Email: email, PasswordHash: hash, Role: role,
		Name: email, CreatedAt: time.Now().UTC(),
	}
	if err := h.store.CreateUser(context.Background(), u); err != nil {
		h.t.Fatalf("create user: %v", err)
	}
	return u
}

func (h *harness) do(method, path string, body io.Reader, cookie *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.serve(h.newRequest(method, path, body, cookie, headers))
}

// newRequest builds a request for the harness handler.
func (h *harness) newRequest(method, path string, body io.Reader, cookie *http.Cookie, headers ...map[string]string) *http.Request {
	h.t.Helper()
	req := httptest.NewRequest(method, path, body)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for _, hs := range headers {
		for k, v := range hs {
			req.Header.Set(k, v)
		}
	}
	return req
}

// serve runs a request through the handler.
func (h *harness) serve(req *http.Request) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func (h *harness) login(email, password string) *http.Cookie {
	h.t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	rec := h.do(http.MethodPost, "/api/auth/login", bytes.NewReader(body), nil, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		h.t.Fatalf("login %s: status %d body %s", email, rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			return c
		}
	}
	h.t.Fatalf("login: no session cookie")
	return nil
}

func newID() string {
	// simple unique id for tests (not UUIDv7, just unique)
	return "id-" + time.Now().Format("150405.000000000") + "-" + randSuffix()
}

var idCounter int

func randSuffix() string {
	idCounter++
	return string(rune('a'+idCounter%26)) + string(rune('a'+(idCounter/26)%26))
}

func jsonBody(v interface{}) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	return v
}

func multipartRevision(data map[string]interface{}, files map[string]string) (*bytes.Buffer, string) {
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	db, _ := json.Marshal(data)
	_ = w.WriteField("data", string(db))
	for name, content := range files {
		fw, _ := w.CreateFormFile("files", name)
		_, _ = fw.Write([]byte(content))
	}
	_ = w.Close()
	return buf, w.FormDataContentType()
}

var _ = os.Stat
