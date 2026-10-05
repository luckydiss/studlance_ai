package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/luckydiss/studlance_ai/internal/auth"
	"github.com/luckydiss/studlance_ai/internal/blobs/fs"
	"github.com/luckydiss/studlance_ai/internal/config"
	"github.com/luckydiss/studlance_ai/internal/httpapi"
	"github.com/luckydiss/studlance_ai/internal/live"
	"github.com/luckydiss/studlance_ai/internal/logging"
	"github.com/luckydiss/studlance_ai/internal/queue"
	"github.com/luckydiss/studlance_ai/internal/store"
	"github.com/luckydiss/studlance_ai/internal/web"
)

func serve(ctx context.Context, cfg config.Server) error {
	if err := os.MkdirAll(cfg.Data, 0o755); err != nil {
		return err
	}
	if logFile := logging.Setup(cfg.Data); logFile != nil {
		defer func() { _ = logFile.Close() }()
	}
	st, err := openStore(ctx, cfg.Data)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	blobStore, err := fs.New(filepath.Join(cfg.Data, "blobs"))
	if err != nil {
		return err
	}

	demoDir := filepath.Join(cfg.Data, "demo")

	healthz := func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		ok := true
		if err := st.Ping(ctx); err != nil {
			ok = false
		}
		if _, err := os.Stat(blobStore.Root()); err != nil {
			ok = false
		}
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"ok":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}

	hub := live.New()
	authSvc := auth.New(st, auth.Config{SessionTTL: cfg.SessionTTL, CookieSecure: cfg.CookieSecure})
	q := queue.New(st, st.DB(), blobStore, hub, nil, queue.Options{
		StageTimeout: cfg.StageTimeout,
		Logger:       slog.Default(),
	})
	api := httpapi.New(st, blobStore, authSvc, hub, q, cfg, slog.Default())
	apiHandler := api.Handler()
	staticHandler := web.Handler(demoDir)

	// Background maintenance: stale leases and stage timeouts (03-lifecycle.md).
	go q.RunSweep(ctx, time.Minute)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthz":
			healthz(w, r)
		case isAPIPath(r.URL.Path):
			if isProtectedAPIPath(r.URL.Path) && !auth.CheckOrigin(r) {
				writeForbiddenJSON(w)
				return
			}
			apiHandler.ServeHTTP(w, r)
		case r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/"):
			if !isAdminRequest(r, authSvc) {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			staticHandler.ServeHTTP(w, r)
		default:
			staticHandler.ServeHTTP(w, r)
		}
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server listening", "addr", cfg.Addr, "data", cfg.Data)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

// isAPIPath reports whether the generated API handler owns this path. Other
// /api/* paths fall through to the web handler, which returns a JSON 404.
func isAPIPath(p string) bool {
	return strings.HasPrefix(p, "/api/auth") ||
		strings.HasPrefix(p, "/api/client") ||
		strings.HasPrefix(p, "/api/admin") ||
		strings.HasPrefix(p, "/api/worker")
}

// isAdminRequest reports whether the request carries an admin session.
func isAdminRequest(r *http.Request, a *auth.Service) bool {
	secret := auth.SessionSecret(r)
	if secret == "" {
		return false
	}
	su, err := a.Resolve(r.Context(), secret)
	if err != nil {
		return false
	}
	return su.User.Role == store.RoleAdmin
}

// isProtectedAPIPath reports whether a path requires an Origin check (CSRF).
func isProtectedAPIPath(p string) bool {
	return strings.HasPrefix(p, "/api/auth") ||
		strings.HasPrefix(p, "/api/client") ||
		strings.HasPrefix(p, "/api/admin")
}

func writeForbiddenJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"Доступ запрещён"}}`))
}
