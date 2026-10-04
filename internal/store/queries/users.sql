-- name: CreateUser :one
INSERT INTO users (id, email, password_hash, role, name, created_at, disabled_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UserByEmail :one
SELECT * FROM users WHERE email = ? COLLATE NOCASE;

-- name: UserByID :one
SELECT * FROM users WHERE id = ?;

-- name: CreateWorker :one
INSERT INTO workers (id, name, token_hash, capabilities, info, created_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: WorkerByName :one
SELECT * FROM workers WHERE name = ?;