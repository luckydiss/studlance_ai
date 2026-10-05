// Package worker implements the execution worker (05-worker.md): the claim
// loop, the per-job working folder, stage runs over codex/claude and the
// snapshot/upload/commit protocol against /api/worker.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/luckydiss/studlance_ai/internal/config"
	"github.com/luckydiss/studlance_ai/internal/worker/client"
	"github.com/luckydiss/studlance_ai/internal/worker/preview"
)

// Worker is the execution worker: one order at a time (05-worker.md).
type Worker struct {
	cfg    config.Worker
	cl     *client.Client
	logger *slog.Logger
	rend   preview.Renderer
	now    func() time.Time
}

// New creates a Worker on top of the API client.
func New(cfg config.Worker, cl *client.Client, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		cfg:    cfg,
		cl:     cl,
		logger: logger,
		rend:   preview.Renderer{PdfToppm: cfg.PdfToppm},
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// Run registers the worker and serves claims until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	caps, info := detectCapabilities(w.cfg)
	info["worker"] = "studlance-worker"
	for {
		workerID, err := w.cl.Register(ctx, caps, info)
		if err == nil {
			w.logger.Info("registered", "worker_id", workerID, "name", w.cfg.Name, "capabilities", caps)
			break
		}
		if errors.Is(err, client.ErrUnauthorized) {
			return errors.New("worker: неверный токен (401) — проверьте token в worker.toml")
		}
		if ctx.Err() != nil {
			return nil
		}
		// Non-retryable 4xx are not expected for register; the client already
		// retried network errors. Pause briefly and try again.
		w.logger.Error("register failed, retrying", "err", err)
		if !sleepCtx(ctx, 2*time.Second) {
			return nil
		}
	}

	for {
		asn, err := w.cl.Claim(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.logger.Error("claim failed, retrying", "err", err)
			if !sleepCtx(ctx, 2*time.Second) {
				return nil
			}
			continue
		}
		if asn == nil {
			continue
		}
		w.logger.Info("claimed", "job", asn.JobId, "action", string(asn.Action), "stage", string(asn.Stage), "attempt", asn.Attempt, "epoch", asn.Epoch)
		w.executeJob(ctx, asn)
		if ctx.Err() != nil {
			return nil
		}
	}
}

// Probe detects capabilities without contacting the server (worker probe).
func Probe(cfg config.Worker) (caps []string, info map[string]interface{}) {
	return detectCapabilities(cfg)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
