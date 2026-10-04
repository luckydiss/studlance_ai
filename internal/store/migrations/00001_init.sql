-- +goose Up
CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,
  role          TEXT NOT NULL CHECK (role IN ('client','admin')),
  name          TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  disabled_at   INTEGER
);

CREATE TABLE sessions (
  id_hash      TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  user_agent   TEXT NOT NULL DEFAULT '',
  ip           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE login_attempts (
  key        TEXT NOT NULL,
  at         INTEGER NOT NULL
);
CREATE INDEX login_attempts_key ON login_attempts(key, at);

CREATE TABLE workers (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  token_hash   TEXT NOT NULL UNIQUE,
  capabilities TEXT NOT NULL DEFAULT '[]',
  info         TEXT NOT NULL DEFAULT '{}',
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER
);

CREATE TABLE jobs (
  id               TEXT PRIMARY KEY,
  user_id          TEXT NOT NULL REFERENCES users(id),
  title            TEXT NOT NULL,
  prompt           TEXT NOT NULL,
  status           TEXT NOT NULL CHECK (status IN ('uploading','queued','running','needs_input','done','failed','canceled')),
  stage            TEXT CHECK (stage IN ('draft','verify','revise')),
  current_version  INTEGER NOT NULL DEFAULT 0,
  pending_revision INTEGER,
  needs_attention  INTEGER NOT NULL DEFAULT 0,
  worker_id        TEXT REFERENCES workers(id),
  lease_epoch      INTEGER NOT NULL DEFAULT 0,
  lease_expires_at INTEGER,
  cancel_requested INTEGER NOT NULL DEFAULT 0,
  attempt          INTEGER NOT NULL DEFAULT 0,
  question         TEXT,
  state            TEXT NOT NULL DEFAULT '{}',
  error            TEXT,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  finished_at      INTEGER
);
CREATE INDEX jobs_user ON jobs(user_id, created_at DESC);
CREATE INDEX jobs_queue ON jobs(status, created_at);

CREATE TABLE files (
  id          TEXT PRIMARY KEY,
  job_id      TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  kind        TEXT NOT NULL CHECK (kind IN ('input','draft','version','log','page','thumb','crop')),
  version     INTEGER NOT NULL DEFAULT 0,
  path        TEXT NOT NULL,
  blob_key    TEXT NOT NULL UNIQUE,
  size        INTEGER NOT NULL,
  sha256      TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  UNIQUE (job_id, kind, version, path)
);

CREATE TABLE documents (
  id           TEXT PRIMARY KEY,
  job_id       TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  snapshot     TEXT NOT NULL CHECK (snapshot IN ('draft','version')),
  version      INTEGER NOT NULL DEFAULT 0,
  idx          INTEGER NOT NULL,
  title        TEXT NOT NULL,
  kind         TEXT NOT NULL,
  file_path    TEXT NOT NULL,
  preview_path TEXT,
  page_count   INTEGER NOT NULL DEFAULT 0,
  UNIQUE (job_id, snapshot, version, idx)
);

CREATE TABLE pages (
  document_id   TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  page          INTEGER NOT NULL,
  image_key     TEXT NOT NULL,
  thumb_key     TEXT NOT NULL,
  width         INTEGER NOT NULL,
  height        INTEGER NOT NULL,
  changed_boxes TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (document_id, page)
);

CREATE TABLE revisions (
  id           TEXT PRIMARY KEY,
  job_id       TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  version      INTEGER NOT NULL,
  comment      TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  completed_at INTEGER,
  UNIQUE (job_id, version)
);

CREATE TABLE revision_remarks (
  id          TEXT PRIMARY KEY,
  revision_id TEXT NOT NULL REFERENCES revisions(id) ON DELETE CASCADE,
  idx         INTEGER NOT NULL,
  document_id TEXT NOT NULL REFERENCES documents(id),
  page        INTEGER NOT NULL,
  x REAL NOT NULL, y REAL NOT NULL, w REAL NOT NULL, h REAL NOT NULL,
  text        TEXT NOT NULL
);

CREATE TABLE agent_runs (
  id            TEXT PRIMARY KEY,
  job_id        TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  version       INTEGER NOT NULL,
  agent         TEXT NOT NULL CHECK (agent IN ('codex','claude')),
  stage         TEXT NOT NULL CHECK (stage IN ('draft','verify','revise')),
  attempt       INTEGER NOT NULL DEFAULT 0,
  session_id    TEXT,
  started_at    INTEGER NOT NULL,
  finished_at   INTEGER,
  exit_code     INTEGER,
  outcome       TEXT CHECK (outcome IN ('ok','question','failed','canceled','timeout')),
  input_tokens  INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cost_usd      REAL NOT NULL DEFAULT 0,
  raw_log_key   TEXT,
  error         TEXT
);
CREATE INDEX agent_runs_job ON agent_runs(job_id, started_at);

CREATE TABLE trace_steps (
  agent_run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
  seq          INTEGER NOT NULL,
  ts           INTEGER NOT NULL,
  type         TEXT NOT NULL CHECK (type IN ('message','reasoning','command','file','web','tool','error','other')),
  summary      TEXT NOT NULL,
  payload      TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY (agent_run_id, seq)
);

CREATE TABLE events (
  id                TEXT PRIMARY KEY,
  job_id            TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  ts                INTEGER NOT NULL,
  kind              TEXT NOT NULL,
  visible_to_client INTEGER NOT NULL DEFAULT 0,
  data              TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX events_job ON events(job_id, ts);

CREATE TABLE job_notes (
  id         TEXT PRIMARY KEY,
  job_id     TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  author_id  TEXT NOT NULL REFERENCES users(id),
  text       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE job_notes;
DROP TABLE events;
DROP TABLE trace_steps;
DROP TABLE agent_runs;
DROP TABLE revision_remarks;
DROP TABLE revisions;
DROP TABLE pages;
DROP TABLE documents;
DROP TABLE files;
DROP TABLE jobs;
DROP TABLE workers;
DROP TABLE login_attempts;
DROP TABLE sessions;
DROP TABLE users;