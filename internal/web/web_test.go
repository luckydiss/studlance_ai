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
		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("%s: content-type %q", path, ct)
		}
		// Either the built app or the "not built" placeholder is acceptable.
		body := rec.Body.String()
		if !strings.Contains(body, "<!doctype html>") && !strings.Contains(body, "<!DOCTYPE html>") {
			t.Fatalf("%s: unexpected body %q", path, body)
		}
	}
}

func TestDemoServesFileAndRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stack-tech.jpg"), []byte("jpegdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file that exists on disk but is not in the whitelist must not be served.
	if err := os.WriteFile(filepath.Join(dir, "secret.jpg"), []byte("hidden"), 0o644); err != nil {
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

	for _, p := range []string{
		"/demo/../studlance.db",
		"/demo/a%5Cb.jpg",
		"/demo/missing.jpg",
		"/demo/secret.jpg", // exists but not whitelisted
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d want 404", p, rec.Code)
		}
	}
}

func TestPlaceholderIsPresent(t *testing.T) {
	body := string(web.Placeholder())
	if !strings.Contains(body, "Фронт не собран") {
		t.Fatalf("placeholder body %q", body)
	}
}

func TestUnknownAPIReturnsJSONNotFound(t *testing.T) {
	h := web.Handler(t.TempDir())

	for _, p := range []string{"/api/unknown", "/api/client/jobs/1"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d want 404", p, rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("%s: content-type %q", p, ct)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"code":"not_found"`) || !strings.Contains(body, "Не найдено") {
			t.Fatalf("%s: body %q", p, body)
		}
	}
}
