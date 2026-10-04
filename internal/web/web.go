// Package web serves the built frontends from go:embed and /demo assets.
package web

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// distFS embeds the built frontends. dist/.keep is committed so go build
// works even without a frontend build; the actual assets are gitignored.
//
//go:embed all:dist
var distFS embed.FS

// placeholder is served with HTTP 200 when a frontend has not been built yet
// (no index.html in dist). It lives outside dist so `pnpm build` never
// overwrites it.
//
//go:embed placeholder.html
var placeholder []byte

// Placeholder returns the "frontend not built" page. It is embedded separately
// from dist so a frontend build never overwrites it.
func Placeholder() []byte { return placeholder }

// Handler serves the client SPA at "/", the admin SPA under /admin, and demo
// images under /demo. demoDir is data/demo; missing files return 404.
func Handler(demoDir string) http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	clientFS := mustSub(sub, "client")
	adminFS := mustSub(sub, "admin")
	return &handler{client: clientFS, admin: adminFS, demoDir: demoDir}
}

type handler struct {
	client  fs.FS
	admin   fs.FS
	demoDir string
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		// handled by server mux before this handler; kept for safety
		http.NotFound(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/"):
		// Unknown API routes must not fall through to the SPA.
		writeNotFound(w)
	case strings.HasPrefix(r.URL.Path, "/demo/"):
		h.serveDemo(w, r)
	case r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/"):
		serveSPA(w, r, h.admin, "/admin")
	default:
		serveSPA(w, r, h.client, "")
	}
}

func writeNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"Не найдено"}}`))
}

// demoFiles whitelists the only names served from /demo/, per the table in
// docs/design/README.md. Anything else is 404.
var demoFiles = map[string]bool{
	"stack-tech.jpg":     true,
	"stack-code.jpg":     true,
	"stack-hum.jpg":      true,
	"how-scheme-a3.jpg":  true,
	"how-page-table.jpg": true,
	"how-page-calc.jpg":  true,
	"how-scheme2-a3.jpg": true,
}

func (h *handler) serveDemo(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/demo/")
	if !demoFiles[name] {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(h.demoDir, name)
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// serveSPA serves a real file if it exists, otherwise index.html (SPA fallback).
func serveSPA(w http.ResponseWriter, r *http.Request, files fs.FS, base string) {
	rel := strings.TrimPrefix(r.URL.Path, base)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		serveIndex(w, r, files)
		return
	}
	if f, err := files.Open(path.Clean(rel)); err == nil {
		st, statErr := f.Stat()
		if statErr == nil && !st.IsDir() {
			if rs, ok := f.(io.ReadSeeker); ok {
				defer func() { _ = f.Close() }()
				if strings.HasPrefix(rel, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				http.ServeContent(w, r, rel, st.ModTime(), rs)
				return
			}
		}
		_ = f.Close()
	}
	serveIndex(w, r, files)
}

func serveIndex(w http.ResponseWriter, r *http.Request, files fs.FS) {
	data, err := fs.ReadFile(files, "index.html")
	if err != nil {
		// Frontend not built: serve the placeholder with 200 so the page loads.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(placeholder)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
