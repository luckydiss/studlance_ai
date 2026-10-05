// Package queue implements the worker queue: claiming orders, leases with
// fencing tokens, status transitions and the background sweeper
// (03-lifecycle.md). HTTP handlers only call into this package; all state
// transitions live here.
//
// Time comes from an injectable Clock so tests never sleep. All queue
// timestamps (leases, events, updated_at inside transitions) use the Clock;
// only the long-poll deadline uses the wall clock.
package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/luckydiss/studlance_ai/internal/id"
	"github.com/luckydiss/studlance_ai/internal/live"
	"github.com/luckydiss/studlance_ai/internal/store"
)

// Clock abstracts the current time.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// Action values of an Assignment (04-api.md).
const (
	ActionStart    = "start"
	ActionContinue = "continue"
	ActionAnswer   = "answer"
	ActionRevise   = "revise"
)

// Finish outcomes (04-api.md).
const (
	OutcomeOK       = "ok"
	OutcomeFailed   = "failed"
	OutcomeCanceled = "canceled"
	OutcomeTimeout  = "timeout"
)

// ErrNoJob is returned by Claim/ClaimWait when no order is available.
var ErrNoJob = errors.New("no job to claim")

// ErrStaleLease is returned when the caller's worker/epoch does not match the
// job's current lease (worker_id must match and epoch must equal
// jobs.lease_epoch). Maps to HTTP 409 with code stale_lease.
var ErrStaleLease = errors.New("stale lease")

// ConflictError is an invalid state transition; the message is client-facing
// Russian (04-api.md). Maps to HTTP 409 with code conflict.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// Options tune the queue.
type Options struct {
	// LeaseTTL is the lease duration granted on claim and extended by
	// heartbeat (default 60 s).
	LeaseTTL time.Duration
	// ClaimWait is the long-poll ceiling (default 25 s).
	ClaimWait time.Duration
	// StageTimeout is the maximum stage duration counted from stage_started;
	// exceeding it fails the stage like finish {timeout} (default 3 h).
	StageTimeout time.Duration
	// LeaseGrace: running jobs whose lease expired longer ago than this get
	// needs_attention = 1 (default 10 min).
	LeaseGrace time.Duration
	// Logger for the background sweeper (default: discard).
	Logger *slog.Logger
}

func (o *Options) defaults() {
	if o.LeaseTTL <= 0 {
		o.LeaseTTL = 60 * time.Second
	}
	if o.ClaimWait <= 0 {
		o.ClaimWait = 25 * time.Second
	}
	if o.StageTimeout <= 0 {
		o.StageTimeout = 3 * time.Hour
	}
	if o.LeaseGrace <= 0 {
		o.LeaseGrace = 10 * time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Queue is the worker-facing order queue.
type Queue struct {
	st    store.Store
	db    *sql.DB
	hub   *live.Hub
	clock Clock
	opts  Options

	mu   sync.Mutex
	wake chan struct{}
}

// New creates a queue on top of the store. db must be the store's own handle
// (sqlite.Store.DB): transitions run in BEGIN IMMEDIATE transactions on a
// dedicated connection.
func New(st store.Store, db *sql.DB, hub *live.Hub, clock Clock, opts Options) *Queue {
	if clock == nil {
		clock = realClock{}
	}
	opts.defaults()
	return &Queue{st: st, db: db, hub: hub, clock: clock, opts: opts}
}

// Wake wakes all long-polling claims. Must be called on every transition to
// queued (submit, answer, revise, admin retry, auto-retry).
func (q *Queue) Wake() {
	q.mu.Lock()
	if q.wake != nil {
		close(q.wake)
		q.wake = nil
	}
	q.mu.Unlock()
}

func (q *Queue) waitChan() chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.wake == nil {
		q.wake = make(chan struct{})
	}
	return q.wake
}

// publishJob notifies SSE subscribers that the job changed.
func (q *Queue) publishJob(jobID string) {
	q.hub.Publish(jobID, live.Event{Name: "job"})
}

// publishSteps notifies admin SSE subscribers about new trace steps.
func (q *Queue) publishSteps(jobID, runID string, steps []store.TraceStep) {
	data, err := json.Marshal(map[string]interface{}{"run_id": runID, "steps": steps})
	if err != nil {
		return
	}
	q.hub.Publish(jobID, live.Event{Name: "steps", Data: data})
}

// ---------- transaction helpers ----------

// immediate runs fn inside a BEGIN IMMEDIATE transaction on a dedicated
// connection (database/sql). fn must use only the passed connection.
func (q *Queue) immediate(ctx context.Context, fn func(c *sql.Conn) error) error {
	conn, err := q.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin immediate: %w", err)
	}
	if err := fn(conn); err != nil {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

const jobColumns = "id, user_id, title, prompt, status, stage, current_version," +
	" pending_revision, needs_attention, worker_id, lease_epoch, lease_expires_at," +
	" cancel_requested, attempt, question, state, error, created_at, updated_at, finished_at"

// scanJob reads one job row.
func scanJob(s interface{ Scan(...interface{}) error }) (store.Job, error) {
	var j store.Job
	var stage, workerID, question, errText sql.NullString
	var pendingRevision sql.NullInt64
	var leaseExpiresAt, finishedAt sql.NullInt64
	var needsAttention, cancelRequested int64
	var createdAt, updatedAt int64
	err := s.Scan(&j.ID, &j.UserID, &j.Title, &j.Prompt, &j.Status, &stage,
		&j.CurrentVersion, &pendingRevision, &needsAttention, &workerID,
		&j.LeaseEpoch, &leaseExpiresAt, &cancelRequested, &j.Attempt, &question,
		&j.State, &errText, &createdAt, &updatedAt, &finishedAt)
	if err != nil {
		return store.Job{}, err
	}
	j.Stage = stage.String
	j.WorkerID = nullStr(workerID)
	j.PendingRevision = nullInt(pendingRevision)
	j.NeedsAttention = needsAttention != 0
	j.CancelRequested = cancelRequested != 0
	j.Question = nullStr(question)
	j.Error = nullStr(errText)
	j.CreatedAt = time.UnixMilli(createdAt).UTC()
	j.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	j.LeaseExpiresAt = nullTime(leaseExpiresAt)
	j.FinishedAt = nullTime(finishedAt)
	return j, nil
}

// getJob loads a job inside a transaction.
func getJob(ctx context.Context, c *sql.Conn, id string) (store.Job, error) {
	return scanJob(c.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = ?", id))
}

func nullStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

func nullInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func nullTime(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.UnixMilli(n.Int64).UTC()
	return &t
}

func nullStringPtr(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}

func nullTimeMs(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UTC().UnixMilli(), Valid: true}
}

// insertEvent appends an event row inside a transaction.
func insertEvent(ctx context.Context, c *sql.Conn, now time.Time, jobID, kind string, visible bool, data map[string]interface{}) error {
	raw := "{}"
	if data != nil {
		if b, err := json.Marshal(data); err == nil {
			raw = string(b)
		}
	}
	vis := int64(0)
	if visible {
		vis = 1
	}
	_, err := c.ExecContext(ctx,
		"INSERT INTO events (id, job_id, ts, kind, visible_to_client, data) VALUES (?, ?, ?, ?, ?, ?)",
		id.New(), jobID, now.UnixMilli(), kind, vis, raw)
	return err
}

// fenced runs fn inside BEGIN IMMEDIATE after verifying the lease: the job
// must belong to the worker, match epoch and be running. The check happens in
// the same transaction as the write — a separate SELECT beforehand would race.
func (q *Queue) fenced(ctx context.Context, jobID, workerID string, epoch int64, fn func(c *sql.Conn, j store.Job) error) error {
	return q.immediate(ctx, func(c *sql.Conn) error {
		j, err := getJob(ctx, c, jobID)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := checkLease(j, workerID, epoch); err != nil {
			return err
		}
		return fn(c, j)
	})
}

// checkLease verifies worker ownership, the fencing epoch and the running status.
func checkLease(j store.Job, workerID string, epoch int64) error {
	if j.WorkerID == nil || *j.WorkerID != workerID || j.LeaseEpoch != epoch {
		return ErrStaleLease
	}
	if j.Status != store.StatusRunning {
		return &ConflictError{Message: "Заказ не выполняется"}
	}
	return nil
}

// CheckLease verifies the worker's lease for reads (input listing, files).
func (q *Queue) CheckLease(ctx context.Context, jobID, workerID string, epoch int64) error {
	j, err := q.job(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	return checkLease(j, workerID, epoch)
}

// job loads a job through the queue's own connection.
func (q *Queue) job(ctx context.Context, jobID string) (store.Job, error) {
	return scanJob(q.db.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = ?", jobID))
}
