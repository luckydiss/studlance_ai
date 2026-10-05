package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/luckydiss/studlance_ai/internal/store"
)

// Assignment is the work handed to a worker on claim (04-api.md).
type Assignment struct {
	JobID          string
	Epoch          int64
	LeaseExpiresAt time.Time
	Action         string
	Stage          string
	Attempt        int64
	Prompt         string
	Version        int64
	State          map[string]interface{}
	Answer         *string
	Revision       *RevisionInfo
}

// RevisionInfo is the rework payload of an Assignment for the revise stage.
type RevisionInfo struct {
	Version int64
	Comment string
	Remarks []RevisionRemark
	Files   []string
}

// RevisionRemark is one marked region with document context.
type RevisionRemark struct {
	Idx           int64
	DocumentTitle string
	FilePath      string
	Page          int64
	X, Y, W, H    float64
	Text          string
}

// errClaimRace marks a lost claim race; the selection is retried.
var errClaimRace = errors.New("claim race")

// Claim tries to take one order for the worker. Returns ErrNoJob when nothing
// is available. Selection order (09-tasks.md, PR 3):
//  1. the worker's running orders with an expired lease (continue);
//  2. the worker's queued orders (answer, revise, auto-retry);
//  3. unassigned queued orders by created_at.
//
// An order with another worker's worker_id is never handed out.
func (q *Queue) Claim(ctx context.Context, workerID, workerName string) (*Assignment, error) {
	var lastErr error
	for range 3 {
		asn, err := q.claimOnce(ctx, workerID, workerName)
		if errors.Is(err, errClaimRace) {
			lastErr = err
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := q.fillRevision(ctx, asn); err != nil {
			return nil, err
		}
		q.publishJob(asn.JobID)
		return asn, nil
	}
	return nil, lastErr
}

// ClaimWait long-polls Claim until an order is available, the ClaimWait
// deadline passes (ErrNoJob) or the request context is canceled. Waiters wake
// on Wake(); the database is re-checked at most every 2 s.
func (q *Queue) ClaimWait(ctx context.Context, workerID, workerName string) (*Assignment, error) {
	deadline := time.Now().Add(q.opts.ClaimWait)
	for {
		ch := q.waitChan()
		asn, err := q.Claim(ctx, workerID, workerName)
		if err == nil {
			return asn, nil
		}
		if !errors.Is(err, ErrNoJob) {
			return nil, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, ErrNoJob
		}
		wait := remaining
		if wait > 2*time.Second {
			wait = 2 * time.Second
		}
		select {
		case <-ch:
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ErrNoJob
		}
	}
}

func (q *Queue) claimOnce(ctx context.Context, workerID, workerName string) (*Assignment, error) {
	now := q.clock.Now()
	var asn *Assignment
	var purgePrefix string
	err := q.immediate(ctx, func(c *sql.Conn) error {
		j, action, err := q.pick(ctx, c, workerID)
		if err != nil {
			return err
		}

		var answer *string
		state := map[string]interface{}{}
		if j.State != "" {
			_ = json.Unmarshal([]byte(j.State), &state)
		}
		if action == "" {
			// A queued order: decide the action from the state.
			if v, ok := state["pending_answer"].(string); ok && v != "" {
				action = ActionAnswer
				answer = &v
			} else if j.Stage == store.StageRevise && j.Attempt == 0 {
				action = ActionRevise
			} else {
				action = ActionStart
			}
		}

		epoch := j.LeaseEpoch + 1
		leaseExpires := now.Add(q.opts.LeaseTTL)
		res, err := c.ExecContext(ctx,
			"UPDATE jobs SET status = 'running', worker_id = ?, lease_epoch = ?, lease_expires_at = ?, updated_at = ?"+
				" WHERE id = ? AND lease_epoch = ? AND (status = 'queued' OR (status = 'running' AND worker_id = ?))",
			workerID, epoch, leaseExpires.UnixMilli(), now.UnixMilli(), j.ID, j.LeaseEpoch, workerID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errClaimRace
		}

		// The stage timeout bills only working time: it starts at the stage
		// start and restarts when an answer is handed over; continue resumes
		// the same stretch, needs_input time is excluded.
		if action != ActionContinue {
			if _, err := c.ExecContext(ctx, "UPDATE jobs SET stage_started_at = ? WHERE id = ?", now.UnixMilli(), j.ID); err != nil {
				return err
			}
		}

		if err := insertEvent(ctx, c, now, j.ID, "claimed", false, map[string]interface{}{
			"worker": workerName, "epoch": epoch,
		}); err != nil {
			return err
		}
		// stage_started marks a fresh stage start; continue/answer resume an
		// already-started stage.
		if action == ActionStart || action == ActionRevise {
			if err := insertEvent(ctx, c, now, j.ID, "stage_started", false, map[string]interface{}{
				"stage": j.Stage, "attempt": j.Attempt,
			}); err != nil {
				return err
			}
			// A fresh attempt rebuilds the stage snapshot from scratch: drop
			// whatever the previous attempt left (files of a failed verify
			// must not leak into the version bundle). Versions already
			// released to the client are never touched.
			purgePrefix = q.purgeStageSnapshot(ctx, c, j)
		}
		if action == ActionAnswer {
			// The answer is consumed with the assignment: drop it from the
			// state in the same transaction.
			delete(state, "pending_answer")
			raw, err := json.Marshal(state)
			if err != nil {
				return err
			}
			if _, err := c.ExecContext(ctx, "UPDATE jobs SET state = ? WHERE id = ?", string(raw), j.ID); err != nil {
				return err
			}
		}

		version := int64(1)
		if j.Stage == store.StageRevise && j.PendingRevision != nil {
			version = *j.PendingRevision
		}
		asn = &Assignment{
			JobID:          j.ID,
			Epoch:          epoch,
			LeaseExpiresAt: leaseExpires,
			Action:         action,
			Stage:          j.Stage,
			Attempt:        j.Attempt,
			Prompt:         j.Prompt,
			Version:        version,
			State:          state,
			Answer:         answer,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if purgePrefix != "" {
		q.purgeBlobs(ctx, purgePrefix)
	}
	return asn, nil
}

// purgeStageSnapshot deletes the DB rows of the stage's uncommitted snapshot
// inside the claim transaction and returns the blob prefix to purge after the
// commit.
func (q *Queue) purgeStageSnapshot(ctx context.Context, c *sql.Conn, j store.Job) string {
	var snap, kind, docSnapshot string
	var version int64
	switch j.Stage {
	case store.StageDraft:
		snap, kind, docSnapshot, version = "draft", store.FileDraft, store.SnapshotDraft, 0
	case store.StageVerify:
		snap, kind, docSnapshot, version = "v1", store.FileVersion, store.SnapshotVersion, 1
	case store.StageRevise:
		if j.PendingRevision == nil {
			return ""
		}
		version = *j.PendingRevision
		snap, kind, docSnapshot = "v"+strconv.FormatInt(version, 10), store.FileVersion, store.SnapshotVersion
	default:
		return ""
	}
	// Versions already released to the client are never purged.
	if docSnapshot == store.SnapshotVersion && version <= j.CurrentVersion {
		return ""
	}
	if _, err := c.ExecContext(ctx,
		"DELETE FROM files WHERE job_id = ? AND kind = ? AND version = ?", j.ID, kind, version); err != nil {
		return ""
	}
	if _, err := c.ExecContext(ctx,
		"DELETE FROM documents WHERE job_id = ? AND snapshot = ? AND version = ?", j.ID, docSnapshot, version); err != nil {
		return ""
	}
	return "jobs/" + j.ID + "/" + snap + "/"
}

// purgeBlobs removes all blobs under prefix (best effort, after the commit).
func (q *Queue) purgeBlobs(ctx context.Context, prefix string) {
	if q.blobs == nil {
		return
	}
	infos, err := q.blobs.List(ctx, prefix)
	if err != nil {
		return
	}
	for _, info := range infos {
		_ = q.blobs.Delete(ctx, info.Key)
	}
}

// pick selects the next job for the worker. A non-empty action means the job
// was already running (continue); queued jobs leave the action decision to
// the caller.
func (q *Queue) pick(ctx context.Context, c *sql.Conn, workerID string) (store.Job, string, error) {
	// 1) this worker's running orders: a claiming worker is free (one order at
	// a time), so it resumes its order even before the lease expires.
	j, err := scanJob(c.QueryRowContext(ctx,
		"SELECT "+jobColumns+" FROM jobs WHERE worker_id = ? AND status = 'running' ORDER BY updated_at LIMIT 1",
		workerID))
	if err == nil {
		return j, ActionContinue, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.Job{}, "", err
	}
	// 2) this worker's queued orders.
	j, err = scanJob(c.QueryRowContext(ctx,
		"SELECT "+jobColumns+" FROM jobs WHERE worker_id = ? AND status = 'queued' ORDER BY created_at LIMIT 1",
		workerID))
	if err == nil {
		return j, "", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.Job{}, "", err
	}
	// 3) unassigned queued orders, oldest first.
	j, err = scanJob(c.QueryRowContext(ctx,
		"SELECT "+jobColumns+" FROM jobs WHERE worker_id IS NULL AND status = 'queued' ORDER BY created_at LIMIT 1"))
	if err == nil {
		return j, "", nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.Job{}, "", ErrNoJob
	}
	return store.Job{}, "", err
}

// fillRevision loads the rework payload for assignments of the revise stage.
// The job is already leased to this worker and revision data is immutable, so
// these reads run outside the claim transaction.
func (q *Queue) fillRevision(ctx context.Context, asn *Assignment) error {
	if asn.Stage != store.StageRevise {
		return nil
	}
	j, err := q.st.JobByID(ctx, asn.JobID)
	if err != nil {
		return err
	}
	if j.PendingRevision == nil {
		return nil
	}
	n := *j.PendingRevision
	revs, err := q.st.RevisionsByJob(ctx, asn.JobID)
	if err != nil {
		return err
	}
	var rev *store.Revision
	for i := range revs {
		if revs[i].Version == n {
			rev = &revs[i]
			break
		}
	}
	if rev == nil {
		return nil
	}
	info := &RevisionInfo{Version: n, Comment: rev.Comment}
	remarks, err := q.st.RemarksByRevision(ctx, rev.ID)
	if err != nil {
		return err
	}
	for _, r := range remarks {
		rm := RevisionRemark{
			Idx: r.Idx, Page: r.Page,
			X: r.X, Y: r.Y, W: r.W, H: r.H, Text: r.Text,
		}
		if doc, dErr := q.st.DocumentByID(ctx, r.DocumentID); dErr == nil {
			rm.DocumentTitle = doc.Title
			rm.FilePath = doc.FilePath
		}
		info.Remarks = append(info.Remarks, rm)
	}
	input, err := q.st.InputFiles(ctx, asn.JobID)
	if err != nil {
		return err
	}
	remarksPrefix := "revision-" + strconv.FormatInt(n, 10) + "/remarks/"
	for _, f := range input {
		p := strings.TrimPrefix(f.Path, "input/")
		if f.Revision == n && !strings.HasPrefix(p, remarksPrefix) {
			info.Files = append(info.Files, p)
		}
	}
	asn.Revision = info
	return nil
}
