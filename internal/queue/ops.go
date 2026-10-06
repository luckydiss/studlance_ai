package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/luckydiss/studlance_ai/internal/id"
	"github.com/luckydiss/studlance_ai/internal/store"
)

// Heartbeat extends the lease to now + LeaseTTL and reports whether a cancel
// was requested (03-lifecycle.md).
func (q *Queue) Heartbeat(ctx context.Context, jobID, workerID string, epoch int64) (time.Time, bool, error) {
	now := q.clock.Now()
	expires := now.Add(q.opts.LeaseTTL)
	var cancel bool
	err := q.fenced(ctx, jobID, workerID, epoch, func(c *sql.Conn, j store.Job) error {
		cancel = j.CancelRequested
		_, err := c.ExecContext(ctx,
			"UPDATE jobs SET lease_expires_at = ?, updated_at = ? WHERE id = ?",
			expires.UnixMilli(), now.UnixMilli(), jobID)
		return err
	})
	return expires, cancel, err
}

// cancelNow applies a requested cancel: it beats failures, timeouts and
// questions in flight (03-lifecycle.md).
func (q *Queue) cancelNow(ctx context.Context, c *sql.Conn, j store.Job, now time.Time) error {
	if _, err := c.ExecContext(ctx,
		"UPDATE jobs SET status = 'canceled', lease_epoch = lease_epoch + 1, lease_expires_at = NULL,"+
			" finished_at = ?, updated_at = ? WHERE id = ?",
		now.UnixMilli(), now.UnixMilli(), j.ID); err != nil {
		return err
	}
	return insertEvent(ctx, c, now, j.ID, "canceled", true, nil)
}

// Question moves running → needs_input, records the question and drops the
// lease (03-lifecycle.md).
func (q *Queue) Question(ctx context.Context, jobID, workerID string, epoch int64, text string) error {
	now := q.clock.Now()
	err := q.fenced(ctx, jobID, workerID, epoch, func(c *sql.Conn, j store.Job) error {
		if j.CancelRequested {
			return q.cancelNow(ctx, c, j, now)
		}
		if _, err := c.ExecContext(ctx,
			"UPDATE jobs SET status = 'needs_input', question = ?, lease_expires_at = NULL, updated_at = ? WHERE id = ?",
			text, now.UnixMilli(), jobID); err != nil {
			return err
		}
		return insertEvent(ctx, c, now, jobID, "question", true, map[string]interface{}{"text": text})
	})
	if err != nil {
		return err
	}
	q.publishJob(jobID)
	return nil
}

// Finish applies the terminal outcome of the current stage (03-lifecycle.md).
func (q *Queue) Finish(ctx context.Context, jobID, workerID string, epoch int64, outcome string, version *int64, errText string) error {
	now := q.clock.Now()
	wake := false
	err := q.fenced(ctx, jobID, workerID, epoch, func(c *sql.Conn, j store.Job) error {
		switch outcome {
		case OutcomeOK:
			return q.finishOK(ctx, c, j, now, version)
		case OutcomeFailed, OutcomeTimeout:
			w, err := q.failStage(ctx, c, j, now, errText)
			wake = w
			return err
		case OutcomeCanceled:
			if !j.CancelRequested {
				return &ConflictError{Message: "Отмена заказа не запрашивалась"}
			}
			if _, err := c.ExecContext(ctx,
				"UPDATE jobs SET status = 'canceled', lease_expires_at = NULL, finished_at = ?, updated_at = ? WHERE id = ?",
				now.UnixMilli(), now.UnixMilli(), j.ID); err != nil {
				return err
			}
			return insertEvent(ctx, c, now, j.ID, "canceled", true, nil)
		default:
			return &ConflictError{Message: "Неизвестный исход: " + outcome}
		}
	})
	if err != nil {
		return err
	}
	q.publishJob(jobID)
	if wake {
		q.Wake()
	}
	return nil
}

// finishOK completes the verify/revise stage and releases the version to the client.
func (q *Queue) finishOK(ctx context.Context, c *sql.Conn, j store.Job, now time.Time, version *int64) error {
	var expected int64
	switch j.Stage {
	case store.StageVerify:
		expected = 1
	case store.StageRevise:
		if j.PendingRevision != nil {
			expected = *j.PendingRevision
		}
	}
	if expected == 0 {
		return &ConflictError{Message: "Этап не производит версию"}
	}
	if version == nil || *version != expected {
		return &ConflictError{Message: fmt.Sprintf("Ожидалась версия %d", expected)}
	}
	var docs int
	if err := c.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM documents WHERE job_id = ? AND snapshot = 'version' AND version = ?",
		j.ID, expected).Scan(&docs); err != nil {
		return err
	}
	if docs == 0 {
		return &ConflictError{Message: fmt.Sprintf("Снимок версии %d не зафиксирован (commit)", expected)}
	}
	if _, err := c.ExecContext(ctx,
		"UPDATE jobs SET status = 'done', current_version = ?, stage = NULL, pending_revision = NULL,"+
			" cancel_requested = 0, lease_expires_at = NULL, finished_at = ?, updated_at = ? WHERE id = ?",
		expected, now.UnixMilli(), now.UnixMilli(), j.ID); err != nil {
		return err
	}
	if j.Stage == store.StageRevise {
		if _, err := c.ExecContext(ctx,
			"UPDATE revisions SET completed_at = ? WHERE job_id = ? AND version = ?",
			now.UnixMilli(), j.ID, expected); err != nil {
			return err
		}
	}
	return insertEvent(ctx, c, now, j.ID, "version_ready", true, map[string]interface{}{"version": expected})
}

// failStage records a stage failure: attempt 0 re-queues with attempt = 1 on
// the same worker (auto-retry), attempt 1 fails the order. A requested cancel
// wins over any failure. The lease epoch is bumped so the old worker
// generation gets 409 stale_lease on any write. Returns wake = true when the
// order went back to queued.
func (q *Queue) failStage(ctx context.Context, c *sql.Conn, j store.Job, now time.Time, errText string) (bool, error) {
	if j.CancelRequested {
		return false, q.cancelNow(ctx, c, j, now)
	}
	if err := insertEvent(ctx, c, now, j.ID, "stage_failed", false, map[string]interface{}{
		"stage": j.Stage, "attempt": j.Attempt, "error": errText,
	}); err != nil {
		return false, err
	}
	if j.Attempt == 0 {
		if _, err := c.ExecContext(ctx,
			"UPDATE jobs SET status = 'queued', attempt = 1, lease_epoch = lease_epoch + 1,"+
				" lease_expires_at = NULL, updated_at = ? WHERE id = ?",
			now.UnixMilli(), j.ID); err != nil {
			return false, err
		}
		if err := insertEvent(ctx, c, now, j.ID, "retried", false, map[string]interface{}{"by": "auto"}); err != nil {
			return false, err
		}
		return true, nil
	}
	if _, err := c.ExecContext(ctx,
		"UPDATE jobs SET status = 'failed', needs_attention = 1, error = ?,"+
			" lease_epoch = lease_epoch + 1, lease_expires_at = NULL, updated_at = ? WHERE id = ?",
		errText, now.UnixMilli(), j.ID); err != nil {
		return false, err
	}
	if err := insertEvent(ctx, c, now, j.ID, "failed", true, map[string]interface{}{"error": errText}); err != nil {
		return false, err
	}
	return false, nil
}

// RegisterWorker updates the worker's capabilities, info and last_seen_at.
func (q *Queue) RegisterWorker(ctx context.Context, workerID string, capabilities []string, info map[string]interface{}) error {
	caps, err := json.Marshal(capabilities)
	if err != nil {
		return err
	}
	inf, err := json.Marshal(info)
	if err != nil {
		return err
	}
	_, err = q.db.ExecContext(ctx,
		"UPDATE workers SET capabilities = ?, info = ?, last_seen_at = ? WHERE id = ?",
		string(caps), string(inf), q.clock.Now().UnixMilli(), workerID)
	return err
}

// CheckSnapshot verifies the lease and that the snapshot matches the current
// stage. It is the fast-fail read gate before streaming an upload; the
// authoritative check repeats inside the recording transaction.
func (q *Queue) CheckSnapshot(ctx context.Context, jobID, workerID string, epoch int64, snap SnapshotRef) error {
	j, err := q.job(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := checkLease(j, workerID, epoch); err != nil {
		return err
	}
	return snapshotGate(j, snap)
}

// CheckCommit is the read gate for commit: like CheckSnapshot, but a repeated
// draft commit is allowed through (CommitSnapshot then applies the no-op).
func (q *Queue) CheckCommit(ctx context.Context, jobID, workerID string, epoch int64, snap SnapshotRef) error {
	j, err := q.job(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := checkLease(j, workerID, epoch); err != nil {
		return err
	}
	if err := snapshotGate(j, snap); err == nil {
		return nil
	}
	if snap.Draft {
		committed, cErr := q.st.DocumentsBySnapshot(ctx, jobID, store.SnapshotDraft, 0)
		if cErr == nil && len(committed) > 0 {
			return nil
		}
	}
	return snapshotGate(j, snap)
}

// ---------- agent runs & trace ----------

// CreateRun registers an agent run.
func (q *Queue) CreateRun(ctx context.Context, jobID, workerID string, epoch int64, r store.AgentRun) (string, error) {
	r.ID = id.New()
	r.JobID = jobID
	err := q.fenced(ctx, jobID, workerID, epoch, func(c *sql.Conn, j store.Job) error {
		_, err := c.ExecContext(ctx,
			"INSERT INTO agent_runs (id, job_id, version, agent, stage, attempt, session_id, started_at)"+
				" VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			r.ID, jobID, r.Version, r.Agent, r.Stage, r.Attempt, nullStringPtr(r.SessionID), r.StartedAt.UnixMilli())
		return err
	})
	if err != nil {
		return "", err
	}
	q.publishJob(jobID)
	return r.ID, nil
}

// PatchRun updates optional run fields.
func (q *Queue) PatchRun(ctx context.Context, jobID, runID, workerID string, epoch int64, p store.AgentRunPatch) error {
	err := q.fenced(ctx, jobID, workerID, epoch, func(c *sql.Conn, j store.Job) error {
		res, err := c.ExecContext(ctx,
			"UPDATE agent_runs SET"+
				" session_id = COALESCE(?, session_id), finished_at = COALESCE(?, finished_at),"+
				" exit_code = COALESCE(?, exit_code), outcome = COALESCE(?, outcome),"+
				" input_tokens = COALESCE(?, input_tokens), output_tokens = COALESCE(?, output_tokens),"+
				" cost_usd = COALESCE(?, cost_usd), error = COALESCE(?, error)"+
				" WHERE id = ? AND job_id = ?",
			nullStringPtr(p.SessionID), nullTimeMs(p.FinishedAt), nullInt64Ptr(p.ExitCode), nullStringPtr(p.Outcome),
			nullInt64Ptr(p.InputTokens), nullInt64Ptr(p.OutputTokens), nullFloat64Ptr(p.CostUSD), nullStringPtr(p.Error),
			runID, jobID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	q.publishJob(jobID)
	return nil
}

// AppendSteps inserts a batch of trace steps; duplicates (run_id, seq) are
// silently ignored. Newly inserted steps are returned for SSE fan-out.
func (q *Queue) AppendSteps(ctx context.Context, jobID, runID, workerID string, epoch int64, steps []store.TraceStep) ([]store.TraceStep, error) {
	var added []store.TraceStep
	err := q.fenced(ctx, jobID, workerID, epoch, func(c *sql.Conn, j store.Job) error {
		var exists int
		if err := c.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM agent_runs WHERE id = ? AND job_id = ?", runID, jobID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return store.ErrNotFound
		}
		for _, st := range steps {
			res, err := c.ExecContext(ctx,
				"INSERT OR IGNORE INTO trace_steps (agent_run_id, seq, ts, type, summary, payload) VALUES (?, ?, ?, ?, ?, ?)",
				runID, st.Seq, st.Ts.UnixMilli(), st.Type, st.Summary, st.Payload)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				added = append(added, st)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(added) > 0 {
		q.publishSteps(jobID, runID, added)
	}
	return added, nil
}

// SaveRunLogKey attaches the raw log blob key to a run.
func (q *Queue) SaveRunLogKey(ctx context.Context, jobID, runID, workerID string, epoch int64, key string) error {
	return q.fenced(ctx, jobID, workerID, epoch, func(c *sql.Conn, j store.Job) error {
		res, err := c.ExecContext(ctx,
			"UPDATE agent_runs SET raw_log_key = ? WHERE id = ? AND job_id = ?", key, runID, jobID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// MergeState merges top-level keys into jobs.state.
func (q *Queue) MergeState(ctx context.Context, jobID, workerID string, epoch int64, updates map[string]interface{}) error {
	return q.fenced(ctx, jobID, workerID, epoch, func(c *sql.Conn, j store.Job) error {
		state := map[string]interface{}{}
		if j.State != "" {
			_ = json.Unmarshal([]byte(j.State), &state)
		}
		for k, v := range updates {
			state[k] = v
		}
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		_, err = c.ExecContext(ctx, "UPDATE jobs SET state = ?, updated_at = ? WHERE id = ?",
			string(raw), q.clock.Now().UnixMilli(), jobID)
		return err
	})
}

// ---------- snapshots ----------

// SnapshotRef is a parsed snapshot path parameter: draft or v<n>.
type SnapshotRef struct {
	Draft   bool
	Version int64
}

// ParseSnapshot parses "draft" or "v<n>".
func ParseSnapshot(s string) (SnapshotRef, error) {
	if s == "draft" {
		return SnapshotRef{Draft: true}, nil
	}
	if strings.HasPrefix(s, "v") {
		n, err := strconv.ParseInt(s[1:], 10, 64)
		if err == nil && n >= 1 {
			return SnapshotRef{Version: n}, nil
		}
	}
	return SnapshotRef{}, &ConflictError{Message: "Неверный снимок: " + s}
}

// snapshotGate verifies the snapshot matches the stage the job is on
// (09 PR 3): draft during draft, v1 during verify, v<pending_revision> during
// revise. Repeated uploads of an already-committed snapshot stay allowed so a
// commit retry after a network failure can re-upload files.
func snapshotGate(j store.Job, snap SnapshotRef) error {
	expected, err := expectedSnapshot(j)
	if err != nil {
		return err
	}
	if snap != expected {
		return &ConflictError{Message: fmt.Sprintf("Сейчас ожидается снимок %s", expected)}
	}
	return nil
}

// expectedSnapshot returns the snapshot the current stage produces.
func expectedSnapshot(j store.Job) (SnapshotRef, error) {
	switch j.Stage {
	case store.StageDraft:
		return SnapshotRef{Draft: true}, nil
	case store.StageVerify:
		return SnapshotRef{Version: 1}, nil
	case store.StageRevise:
		if j.PendingRevision != nil && *j.PendingRevision >= 1 {
			return SnapshotRef{Version: *j.PendingRevision}, nil
		}
	}
	return SnapshotRef{}, &ConflictError{Message: "Снимки сейчас не принимаются"}
}

// String renders the snapshot path form (draft | v<n>).
func (s SnapshotRef) String() string {
	if s.Draft {
		return "draft"
	}
	return "v" + strconv.FormatInt(s.Version, 10)
}

// RecordSnapshotFile registers an uploaded snapshot file in the files table.
func (q *Queue) RecordSnapshotFile(ctx context.Context, jobID, workerID string, epoch int64, snap SnapshotRef, path, blobKey string, size int64, sha string) error {
	now := q.clock.Now()
	return q.fenced(ctx, jobID, workerID, epoch, func(c *sql.Conn, j store.Job) error {
		if err := snapshotGate(j, snap); err != nil {
			return err
		}
		kind := store.FileVersion
		version := snap.Version
		if snap.Draft {
			kind = store.FileDraft
			version = 0
		}
		_, err := c.ExecContext(ctx,
			"INSERT INTO files (id, job_id, kind, version, path, blob_key, size, sha256, created_at)"+
				" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)"+
				" ON CONFLICT (job_id, kind, version, path) DO UPDATE SET blob_key = excluded.blob_key,"+
				" size = excluded.size, sha256 = excluded.sha256",
			id.New(), jobID, kind, version, path, blobKey, size, sha, now.UnixMilli())
		return err
	})
}

// RecordRevisionCrop registers an uploaded remark crop. Crops live in their
// own namespace — path revision-crops/<n>/<idx>.png, blob
// jobs/<job_id>/revision-crops/<n>/<idx>.png — so they never share a blob key
// with the client's input files (02-data.md).
func (q *Queue) RecordRevisionCrop(ctx context.Context, jobID, workerID string, epoch, n, idx int64, blobKey string, size int64, sha string) error {
	now := q.clock.Now()
	return q.fenced(ctx, jobID, workerID, epoch, func(c *sql.Conn, j store.Job) error {
		if j.PendingRevision == nil || *j.PendingRevision != n {
			return &ConflictError{Message: fmt.Sprintf("Сейчас не ждут вырезки доработки %d", n)}
		}
		var exists int
		if err := c.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM revision_remarks r JOIN revisions v ON v.id = r.revision_id"+
				" WHERE v.job_id = ? AND v.version = ? AND r.idx = ?",
			jobID, n, idx).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return &ConflictError{Message: fmt.Sprintf("Замечания %d в доработке %d нет", idx, n)}
		}
		path := fmt.Sprintf("revision-crops/%d/%d.png", n, idx)
		_, err := c.ExecContext(ctx,
			"INSERT INTO files (id, job_id, kind, version, path, blob_key, size, sha256, created_at)"+
				" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)"+
				" ON CONFLICT (job_id, kind, version, path) DO UPDATE SET blob_key = excluded.blob_key,"+
				" size = excluded.size, sha256 = excluded.sha256",
			id.New(), jobID, store.FileCrop, n, path, blobKey, size, sha, now.UnixMilli())
		return err
	})
}

// CommitSnapshot fixes a snapshot: replaces its documents and pages in one
// transaction (a repeated commit is idempotent). For draft it also advances
// the stage to verify. Verification JSON for v<n> is written by the caller.
func (q *Queue) CommitSnapshot(ctx context.Context, jobID, workerID string, epoch int64, snap SnapshotRef, title *string, docs []store.DocumentWithPages) error {
	now := q.clock.Now()
	err := q.fenced(ctx, jobID, workerID, epoch, func(c *sql.Conn, j store.Job) error {
		snapshot := store.SnapshotVersion
		version := snap.Version
		if snap.Draft {
			snapshot = store.SnapshotDraft
			version = 0
		}
		var committed int
		if err := c.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM documents WHERE job_id = ? AND snapshot = ? AND version = ?",
			jobID, snapshot, version).Scan(&committed); err != nil {
			return err
		}
		if snap.Draft && j.Stage != store.StageDraft && committed > 0 {
			// Repeated draft commit after a lost response: the draft is
			// already fixed and the order moved on — succeed without changes.
			return nil
		}
		if err := snapshotGate(j, snap); err != nil {
			return err
		}

		if _, err := c.ExecContext(ctx,
			"DELETE FROM documents WHERE job_id = ? AND snapshot = ? AND version = ?", jobID, snapshot, version); err != nil {
			return err
		}
		for _, d := range docs {
			if _, err := c.ExecContext(ctx,
				"INSERT INTO documents (id, job_id, snapshot, version, idx, title, kind, file_path, preview_path, page_count)"+
					" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				d.Document.ID, jobID, snapshot, version, d.Document.Idx, d.Document.Title, d.Document.Kind,
				d.Document.FilePath, nullStringPtr(d.Document.PreviewPath), d.Document.PageCount); err != nil {
				return err
			}
			for _, p := range d.Pages {
				if _, err := c.ExecContext(ctx,
					"INSERT INTO pages (document_id, page, image_key, thumb_key, width, height, changed_boxes)"+
						" VALUES (?, ?, ?, ?, ?, ?, ?)",
					d.Document.ID, p.Page, p.ImageKey, p.ThumbKey, p.Width, p.Height, p.ChangedBoxes); err != nil {
					return err
				}
			}
		}

		if snap.Draft && committed == 0 {
			// First draft commit: draft -> verify (03-lifecycle.md). The order
			// stays running; the version is not visible to the client yet.
			newTitle := j.Title
			if title != nil {
				newTitle = trimTitle(*title)
			}
			if _, err := c.ExecContext(ctx,
				"UPDATE jobs SET stage = 'verify', attempt = 0, title = ?, stage_started_at = ?, updated_at = ? WHERE id = ?",
				newTitle, now.UnixMilli(), now.UnixMilli(), jobID); err != nil {
				return err
			}
			summaries := make([]map[string]interface{}, 0, len(docs))
			for _, d := range docs {
				summaries = append(summaries, map[string]interface{}{
					"idx": d.Document.Idx, "title": d.Document.Title, "kind": d.Document.Kind,
					"file_path": d.Document.FilePath, "page_count": d.Document.PageCount,
				})
			}
			if err := insertEvent(ctx, c, now, jobID, "draft_ready", false, map[string]interface{}{"documents": summaries}); err != nil {
				return err
			}
			if err := insertEvent(ctx, c, now, jobID, "stage_started", false, map[string]interface{}{
				"stage": store.StageVerify, "attempt": 0,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	q.publishJob(jobID)
	return nil
}

// trimTitle applies the commit title rule: first line, at most 120 runes
// (05-worker.md).
func trimTitle(t string) string {
	t = strings.TrimSpace(t)
	if i := strings.IndexAny(t, "\r\n"); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	t = strings.TrimPrefix(t, "#")
	t = strings.TrimSpace(t)
	r := []rune(t)
	if len(r) > 120 {
		r = r[:120]
	}
	return string(r)
}

func nullInt64Ptr(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func nullFloat64Ptr(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}
