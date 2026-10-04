package web_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckydiss/studlance_ai/internal/web"
)

func TestSPAFallbackClientAndAdmin(t *testing.T) {
	h := web.Handler(t.TempDir())

	for _, path := range []string{"/", "/orders/123", "/admin", "/admin/jobs/1"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Фронт не собран") {
			t.Fatalf("%s: unexpected body", path)
		}
	}
}

func TestDemoServesFileAndRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stack-tech.jpg"), []byte("jpegdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := web.Handler(dir)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/demo/stack-tech.jpg", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "jpegdata" {
		t.Fatalf("demo: status %d body %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=86400" {
		t.Fatalf("cache-control %q", cc)
	}

	for _, p := range []string{"/demo/../studlance.db", "/demo/a%5Cb.jpg", "/demo/missing.jpg"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d want 404", p, rec.Code)
		}
	}
}
