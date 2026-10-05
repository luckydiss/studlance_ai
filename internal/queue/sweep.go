package queue

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/luckydiss/studlance_ai/internal/store"
)

// Sweep runs one background maintenance pass (03-lifecycle.md):
//   - running jobs whose lease expired more than LeaseGrace ago get
//     needs_attention = 1 (the client view does not change);
//   - a stage that started more than StageTimeout ago (by its stage_started
//     event) is failed like finish {timeout}; the old lease epoch is
//     invalidated.
func (q *Queue) Sweep(ctx context.Context) error {
	if err := q.markStaleLeases(ctx); err != nil {
		return err
	}
	return q.failTimedOutStages(ctx)
}

// RunSweep runs Sweep every interval until ctx is done (serve starts it once
// per minute).
func (q *Queue) RunSweep(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := q.Sweep(ctx); err != nil {
				q.opts.Logger.Error("sweep failed", "err", err)
			}
		}
	}
}

// markStaleLeases flags running jobs with a long-expired lease.
func (q *Queue) markStaleLeases(ctx context.Context) error {
	now := q.clock.Now()
	cutoff := now.Add(-q.opts.LeaseGrace).UnixMilli()
	var ids []string
	err := q.immediate(ctx, func(c *sql.Conn) error {
		rows, err := c.QueryContext(ctx,
			"SELECT id FROM jobs WHERE status = 'running' AND needs_attention = 0"+
				" AND lease_expires_at IS NOT NULL AND lease_expires_at < ?", cutoff)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, jobID := range ids {
			if _, err := c.ExecContext(ctx,
				"UPDATE jobs SET needs_attention = 1, updated_at = ? WHERE id = ?", now.UnixMilli(), jobID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, jobID := range ids {
		q.publishJob(jobID)
	}
	return nil
}

// failTimedOutStages fails running stages whose working time (from
// stage_started_at) exceeded StageTimeout. Time in needs_input is not billed:
// stage_started_at restarts when the answer is handed to the worker.
func (q *Queue) failTimedOutStages(ctx context.Context) error {
	now := q.clock.Now()
	rows, err := q.db.QueryContext(ctx,
		"SELECT "+jobColumns+" FROM jobs WHERE status = 'running' AND stage IS NOT NULL"+
			" AND stage_started_at IS NOT NULL AND stage_started_at < ?",
		now.Add(-q.opts.StageTimeout).UnixMilli())
	if err != nil {
		return err
	}
	var jobs []store.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, j := range jobs {
		if err := q.timeoutStage(ctx, j, now); err != nil {
			return err
		}
		q.publishJob(j.ID)
		q.Wake()
	}
	return nil
}

// timeoutStage applies the stage-timeout failure to one job, re-checking the
// stage is still the same inside the transaction.
func (q *Queue) timeoutStage(ctx context.Context, j store.Job, now time.Time) error {
	errText := fmt.Sprintf("превышен таймаут этапа %s", formatHours(q.opts.StageTimeout))
	return q.immediate(ctx, func(c *sql.Conn) error {
		cur, err := getJob(ctx, c, j.ID)
		if err != nil {
			return err
		}
		if cur.Status != store.StatusRunning || cur.Stage != j.Stage || cur.Attempt != j.Attempt {
			return nil // changed meanwhile
		}
		_, err = q.failStage(ctx, c, cur, now, errText)
		return err
	})
}

// formatHours renders a duration like 3h as "3 ч" (05-worker.md error text).
func formatHours(d time.Duration) string {
	hours := int(d.Hours())
	if time.Duration(hours)*time.Hour == d && hours > 0 {
		return fmt.Sprintf("%d ч", hours)
	}
	return d.String()
}
