-- name: CreateJob :one
INSERT INTO jobs (id, user_id, title, prompt, status, stage, current_version,
                  pending_revision, needs_attention, worker_id, lease_epoch,
                  lease_expires_at, cancel_requested, attempt, question, state,
                  error, created_at, updated_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: JobByID :one
SELECT * FROM jobs WHERE id = ?;

-- name: JobByIDForUser :one
SELECT * FROM jobs WHERE id = ? AND user_id = ?;

-- name: JobsByUser :many
SELECT * FROM jobs WHERE user_id = ? ORDER BY created_at DESC;

-- name: AdminJobs :many
SELECT * FROM jobs
WHERE (sqlc.narg('status') IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.arg('attention') = 0 OR needs_attention = 1)
  AND (sqlc.narg('query') IS NULL OR title LIKE sqlc.narg('query') OR user_id IN (
        SELECT id FROM users WHERE email LIKE sqlc.narg('query') OR name LIKE sqlc.narg('query')))
ORDER BY created_at DESC;

-- name: UpdateJobStatus :exec
UPDATE jobs
SET status = ?, stage = ?, current_version = ?, pending_revision = ?,
    needs_attention = ?, attempt = ?, question = ?, error = ?,
    cancel_requested = ?, updated_at = ?, finished_at = ?
WHERE id = ?;

-- name: UpdateJobCancelRequested :exec
UPDATE jobs SET cancel_requested = 1, updated_at = ? WHERE id = ?;

-- name: UpdateJobAttention :exec
UPDATE jobs SET needs_attention = ?, updated_at = ? WHERE id = ?;

-- name: UpdateJobState :exec
UPDATE jobs SET state = ?, updated_at = ? WHERE id = ?;

-- name: UpdateJobTitle :exec
UPDATE jobs SET title = ?, updated_at = ? WHERE id = ?;

-- name: UpdateJobAnswer :exec
UPDATE jobs
SET status = 'queued', question = NULL, state = ?, pending_revision = NULL,
    updated_at = ?
WHERE id = ?;

-- name: JobsByWorker :many
SELECT * FROM jobs WHERE worker_id = ? AND status = 'running';

-- name: CountUserJobs :many
SELECT u.id AS user_id, COUNT(j.id) AS jobs_total, MAX(j.created_at) AS last_job_at
FROM users u
LEFT JOIN jobs j ON j.user_id = u.id
WHERE u.role = 'client'
GROUP BY u.id;

-- name: ListClients :many
SELECT id, email, name FROM users WHERE role = 'client' ORDER BY created_at;

-- name: ListWorkers :many
SELECT * FROM workers ORDER BY name;

-- name: WorkerByID :one
SELECT * FROM workers WHERE id = ?;

-- name: CurrentJobForWorker :one
SELECT id FROM jobs WHERE worker_id = ? AND status IN ('running','queued','needs_input')
ORDER BY updated_at DESC LIMIT 1;

-- name: CreateFile :one
INSERT INTO files (id, job_id, kind, version, path, blob_key, size, sha256, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: FileByBlobKey :one
SELECT * FROM files WHERE blob_key = ?;

-- name: FilesByJob :many
SELECT * FROM files WHERE job_id = ? ORDER BY created_at;

-- name: FilesByJobKind :many
SELECT * FROM files WHERE job_id = ? AND kind = ? ORDER BY version, path;

-- name: FileByJobKindPath :one
SELECT * FROM files WHERE job_id = ? AND kind = ? AND path = ?;

-- name: DeleteFileByJobKindPath :exec
DELETE FROM files WHERE job_id = ? AND kind = ? AND path = ?;

-- name: DeleteFileByID :exec
DELETE FROM files WHERE id = ?;

-- name: SumInputBytes :one
SELECT COALESCE(SUM(size), 0) FROM files WHERE job_id = ? AND kind = 'input';

-- name: CreateDocument :one
INSERT INTO documents (id, job_id, snapshot, version, idx, title, kind, file_path,
                       preview_path, page_count)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: DocumentsBySnapshot :many
SELECT * FROM documents WHERE job_id = ? AND snapshot = ? AND version = ? ORDER BY idx;

-- name: DocumentByID :one
SELECT * FROM documents WHERE id = ?;

-- name: DeleteDocumentsBySnapshot :exec
DELETE FROM documents WHERE job_id = ? AND snapshot = ? AND version = ?;

-- name: CreatePage :exec
INSERT INTO pages (document_id, page, image_key, thumb_key, width, height, changed_boxes)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: PagesByDocument :many
SELECT * FROM pages WHERE document_id = ? ORDER BY page;

-- name: DeletePagesByDocument :exec
DELETE FROM pages WHERE document_id = ?;

-- name: CreateRevision :one
INSERT INTO revisions (id, job_id, version, comment, created_at, completed_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: RevisionsByJob :many
SELECT * FROM revisions WHERE job_id = ? ORDER BY version;

-- name: RevisionByJobVersion :one
SELECT * FROM revisions WHERE job_id = ? AND version = ?;

-- name: CreateRevisionRemark :one
INSERT INTO revision_remarks (id, revision_id, idx, document_id, page, x, y, w, h, text)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: RemarksByRevision :many
SELECT * FROM revision_remarks WHERE revision_id = ? ORDER BY idx;

-- name: CreateEvent :one
INSERT INTO events (id, job_id, ts, kind, visible_to_client, data)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: EventsByJob :many
SELECT * FROM events WHERE job_id = ? ORDER BY ts;

-- name: CreateNote :one
INSERT INTO job_notes (id, job_id, author_id, text, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: NotesByJob :many
SELECT * FROM job_notes WHERE job_id = ? ORDER BY created_at;

-- name: CreateSession :exec
INSERT INTO sessions (id_hash, user_id, created_at, expires_at, last_seen_at, user_agent, ip)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: SessionByIDHash :one
SELECT * FROM sessions WHERE id_hash = ?;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id_hash = ?;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = ?;

-- name: CountLoginAttempts :one
SELECT COUNT(*) FROM login_attempts WHERE key = ? AND at > ?;

-- name: CreateLoginAttempt :exec
INSERT INTO login_attempts (key, at) VALUES (?, ?);

-- name: DeleteLoginAttempts :exec
DELETE FROM login_attempts WHERE key = ?;

-- name: DeleteOldLoginAttempts :exec
DELETE FROM login_attempts WHERE at < ?;

-- name: WorkerByTokenHash :one
SELECT * FROM workers WHERE token_hash = ?;

-- name: TouchWorker :exec
UPDATE workers SET last_seen_at = ? WHERE id = ?;

-- name: CreateAgentRun :one
INSERT INTO agent_runs (id, job_id, version, agent, stage, attempt, session_id,
                        started_at, finished_at, exit_code, outcome, input_tokens,
                        output_tokens, cost_usd, raw_log_key, error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: AgentRunByID :one
SELECT * FROM agent_runs WHERE id = ? AND job_id = ?;

-- name: AgentRunsByJob :many
SELECT * FROM agent_runs WHERE job_id = ? ORDER BY started_at;

-- name: PatchAgentRun :exec
UPDATE agent_runs SET
  session_id = COALESCE(sqlc.narg('session_id'), session_id),
  finished_at = COALESCE(sqlc.narg('finished_at'), finished_at),
  exit_code = COALESCE(sqlc.narg('exit_code'), exit_code),
  outcome = COALESCE(sqlc.narg('outcome'), outcome),
  input_tokens = COALESCE(sqlc.narg('input_tokens'), input_tokens),
  output_tokens = COALESCE(sqlc.narg('output_tokens'), output_tokens),
  cost_usd = COALESCE(sqlc.narg('cost_usd'), cost_usd),
  error = COALESCE(sqlc.narg('error'), error)
WHERE id = ? AND job_id = ?;

-- name: CreateTraceStep :exec
INSERT OR IGNORE INTO trace_steps (agent_run_id, seq, ts, type, summary, payload)
VALUES (?, ?, ?, ?, ?, ?);

-- name: TraceStepsByRun :many
SELECT * FROM trace_steps WHERE agent_run_id = ? AND seq > ? ORDER BY seq LIMIT ?;

-- name: SaveRawLogKey :exec
UPDATE agent_runs SET raw_log_key = ? WHERE id = ? AND job_id = ?;