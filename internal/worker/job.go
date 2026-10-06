package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/luckydiss/studlance_ai/internal/httpapi"
	"github.com/luckydiss/studlance_ai/internal/worker/client"
	"github.com/luckydiss/studlance_ai/prompts"
)

// stageOutcome is the end of one stage attempt.
type stageOutcome int

const (
	outcomeSuccess  stageOutcome = iota // stage done, snapshot/commit follow
	outcomeQuestion                     // QUESTIONS.md appeared — question posted
	outcomeFailed                       // stage failed — finish failed/timeout
	outcomeCanceled                     // cancel requested — finish canceled
	outcomeAborted                      // stale lease or worker shutdown — no finish
)

type stageResult struct {
	outcome stageOutcome
	errText string
	timeout bool
}

// jobExec runs one claimed assignment.
type jobExec struct {
	w   *Worker
	asn *httpapi.Assignment
	dir string
	log *slog.Logger

	stage       string // current stage (draft|verify|revise)
	inputFiles  []httpapi.WorkerInputFile
	names       *localNames // server path -> unique local path
	state       localState
	agentCancel context.CancelFunc // cancels the agent's stage context

	// jobCtx is the service context of the job: heartbeat, run patch,
	// question and the terminal finish (including finish canceled, which must
	// survive the cancellation that caused it).
	jobCtx context.Context
	// workCtx is the context of the job's active work: input downloads,
	// snapshot and crop uploads, commit, create-run, steps and finish ok. It
	// is canceled as soon as a cancellation is confirmed or the lease is
	// lost, so no new HTTP attempt (including the client's internal retries
	// after 5xx or a lost response) is started after that.
	workCtx    context.Context
	workCancel context.CancelFunc

	cancelRequested atomic.Bool
	stale           atomic.Bool
	stageTimeout    atomic.Bool
}

// activeCtx returns the context of the job's active work. Falls back to ctx
// for jobExec values built directly by tests.
func (j *jobExec) activeCtx(ctx context.Context) context.Context {
	if j.workCtx != nil {
		return j.workCtx
	}
	return ctx
}

// serviceCtx returns the context of the job's service requests; it stays
// usable after a cancellation so the canceled outcome can be reported.
func (j *jobExec) serviceCtx(ctx context.Context) context.Context {
	if j.jobCtx != nil {
		return j.jobCtx
	}
	return ctx
}

// stopWork cancels the active work context; idempotent.
func (j *jobExec) stopWork() {
	if j.workCancel != nil {
		j.workCancel()
	}
}

// executeJob handles one assignment end to end: prepare, stages, after-stage.
func (w *Worker) executeJob(ctx context.Context, asn *httpapi.Assignment) {
	j := &jobExec{
		w:   w,
		asn: asn,
		dir: filepath.Join(w.cfg.WorkDir, asn.JobId),
		log: w.logger.With("job", asn.JobId),
	}
	j.run(ctx)
}

func (j *jobExec) run(ctx context.Context) {
	j.jobCtx = ctx
	workCtx, workCancel := context.WithCancel(ctx)
	j.workCtx, j.workCancel = workCtx, workCancel
	defer workCancel()

	if err := j.prepare(j.activeCtx(ctx)); err != nil {
		j.log.Error("prepare failed", "err", err)
		if isStale(err) {
			return
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errCanceled) {
			j.finish(ctx, httpapi.FinishRequestOutcomeCanceled, nil, "")
			return
		}
		j.finish(ctx, httpapi.FinishRequestOutcomeFailed, nil, "не удалось подготовить папку заказа: "+err.Error())
		return
	}

	// Server state + prompts version for the admin view (service request).
	if err := j.setState(j.serviceCtx(ctx), map[string]interface{}{"prompts_version": prompts.PromptsVersion}); err != nil {
		if isStale(err) {
			return
		}
		j.log.Error("set state", "err", err)
	}

	agentCtx, agentCancel := context.WithCancel(ctx)
	j.agentCancel = agentCancel
	defer agentCancel()

	hbDone := make(chan struct{})
	go j.heartbeatLoop(ctx, hbDone)
	defer close(hbDone)

	action := string(j.asn.Action)
	j.stage = string(j.asn.Stage)
	for {
		res := j.runStage(ctx, agentCtx, j.stage, action)
		switch res.outcome {
		case outcomeAborted:
			return
		case outcomeCanceled:
			j.finish(ctx, httpapi.FinishRequestOutcomeCanceled, nil, "")
			return
		case outcomeQuestion:
			return // question already posted
		case outcomeFailed:
			outcome := httpapi.FinishRequestOutcomeFailed
			if res.timeout {
				outcome = httpapi.FinishRequestOutcomeTimeout
			}
			j.finish(ctx, outcome, nil, res.errText)
			return
		}
		// Success: after-stage by stage kind (05-worker.md «После этапа»).
		// A cancel that arrived after the agent exited still wins over commit.
		if j.cancelRequested.Load() {
			j.finish(ctx, httpapi.FinishRequestOutcomeCanceled, nil, "")
			return
		}
		switch j.stage {
		case "draft":
			if !j.commitDraft(j.activeCtx(ctx)) {
				return
			}
			// Straight into verify on the same lease, without a new claim.
			// The server reset attempt to 0 at the draft commit.
			j.stage = "verify"
			action = "start"
			j.asn.Attempt = 0
			j.state.StageStartedAt = time.Time{}
			j.saveState()
			j.log.Info("draft committed, starting verify")
		case "verify":
			v := 1
			if !j.commitVersion(j.activeCtx(ctx), v, "draft") {
				return // already finished (canceled or failed)
			}
			if j.cancelRequested.Load() {
				j.finishCanceledIfRequested(ctx)
				return
			}
			j.finishOk(ctx, &v)
			return
		case "revise":
			v := j.asn.Version
			prev := fmt.Sprintf("v%d", v-1)
			if !j.commitVersion(j.activeCtx(ctx), v, prev) {
				return // already finished (canceled or failed)
			}
			if j.cancelRequested.Load() {
				j.finishCanceledIfRequested(ctx)
				return
			}
			j.finishOk(ctx, &v)
			return
		default:
			j.log.Error("unknown stage", "stage", j.stage)
			return
		}
	}
}

// errCanceled marks work stopped because the client canceled the order: no
// further preparation, upload or commit may happen after it is known.
var errCanceled = errors.New("отменено")

// finishCanceledIfRequested reports canceled when the flag is set.
func (j *jobExec) finishCanceledIfRequested(ctx context.Context) {
	if j.cancelRequested.Load() && !j.stale.Load() {
		j.finish(ctx, httpapi.FinishRequestOutcomeCanceled, nil, "")
	}
}

// finishOk reports success unless the order was canceled in the meantime (the
// cancel flag always wins over a successful commit) or the lease was lost
// (then there is nothing to report). finish re-evaluates the cancel flag after
// a failed send, so a cancellation during the finish-ok request ends canceled.
func (j *jobExec) finishOk(ctx context.Context, version *int) {
	if j.stale.Load() {
		return
	}
	if j.cancelRequested.Load() {
		j.finish(ctx, httpapi.FinishRequestOutcomeCanceled, nil, "")
		return
	}
	j.finish(ctx, httpapi.FinishRequestOutcomeOk, version, "")
}

// heartbeatLoop extends the lease and watches for cancellation and fencing.
func (j *jobExec) heartbeatLoop(ctx context.Context, done chan struct{}) {
	t := time.NewTicker(j.w.cfg.HeartbeatDuration())
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-t.C:
		}
		resp, err := j.w.cl.Heartbeat(ctx, j.asn.JobId, j.asn.Epoch)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Stale lease or "order not running": drop the job at once and
			// stop every pending work request.
			if isStale(err) || isConflict(err) {
				j.log.Error("heartbeat lost the lease", "err", err)
				j.stale.Store(true)
				j.stopWork()
				j.agentCancel()
				return
			}
			j.log.Error("heartbeat", "err", err)
			continue
		}
		if resp.Cancel {
			j.log.Info("cancel requested")
			j.cancelRequested.Store(true)
			j.stopWork()
			j.agentCancel()
			return
		}
	}
}

// finish reports the terminal outcome; best-effort logs on top of it.
//
// The cancel flag is evaluated before the first attempt and re-evaluated after
// a failed one: when the cancellation is confirmed while the terminal request
// or its retry backoff is in flight, the original ok/failed/timeout is not
// repeated and canceled is reported on the service context instead. The
// context is chosen by the outcome: canceled goes out on the service context
// (it must survive the cancellation that caused it), everything else on the
// active work context, so a cancellation stops their retries.
func (j *jobExec) finish(ctx context.Context, outcome httpapi.FinishRequestOutcome, version *int, errText string) {
	if j.stale.Load() {
		return
	}
	if j.cancelRequested.Load() && outcome != httpapi.FinishRequestOutcomeCanceled {
		outcome, version, errText = httpapi.FinishRequestOutcomeCanceled, nil, ""
	}
	cctx := j.activeCtx(ctx)
	if outcome == httpapi.FinishRequestOutcomeCanceled {
		cctx = j.serviceCtx(ctx)
	}
	err := j.sendFinish(cctx, outcome, version, errText)
	if err == nil {
		j.log.Info("finished", "outcome", outcome, "error", errText)
		return
	}
	j.log.Error("finish", "outcome", outcome, "err", err)
	if outcome == httpapi.FinishRequestOutcomeCanceled {
		// The service request already retried as far as its context allowed;
		// there is nothing else to report.
		return
	}
	// The original ok/failed/timeout died because the cancellation canceled
	// the work context. Do not repeat it: report canceled instead. A lost
	// lease or a shutting-down worker (Ctrl+C) reports nothing at all.
	if j.stale.Load() || j.serviceCtx(ctx).Err() != nil {
		return
	}
	if !j.cancelRequested.Load() {
		return
	}
	j.finish(ctx, httpapi.FinishRequestOutcomeCanceled, nil, "")
}

// sendFinish posts one finish request.
func (j *jobExec) sendFinish(ctx context.Context, outcome httpapi.FinishRequestOutcome, version *int, errText string) error {
	req := httpapi.FinishRequest{Epoch: j.asn.Epoch, Outcome: outcome, Version: version}
	if errText != "" {
		req.Error = &errText
	}
	return j.w.cl.Finish(ctx, j.asn.JobId, req)
}

func isConflict(err error) bool {
	var ce *client.ConflictError
	return errors.As(err, &ce)
}

// humanDuration renders «3 ч» / «15 мин» for error strings.
func humanDuration(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%d ч", int(d/time.Hour))
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d мин", int(d/time.Minute))
	}
	return d.String()
}

// ---------- helpers shared by stage/snapshot code ----------

func readFileTrimmed(path string, limit int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(raw))
	r := []rune(s)
	if len(r) > limit {
		s = string(r[:limit])
	}
	return s
}
