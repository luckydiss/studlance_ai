# 02. Данные

## Общие правила

- SQLite, один файл `data/studlance.db`, `PRAGMA journal_mode=WAL; foreign_keys=ON; busy_timeout=5000`.
- Идентификаторы — UUIDv7 строкой (`TEXT`), кроме `trace_steps` (составной ключ).
- Время — `INTEGER`, миллисекунды Unix UTC. В API отдаётся как RFC 3339.
- JSON-поля — `TEXT` с валидным JSON.
- Все запросы клиента к своим данным содержат `user_id = ?` в самом SQL (не фильтр после выборки).
- Доступ к БД только через `store.Store` (интерфейс в `internal/store/store.go`); реализация `internal/store/sqlite`. Логика не знает про SQLite.

## Схема (первая миграция `00001_init.sql`)

```sql
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
  id_hash      TEXT PRIMARY KEY,              -- sha256(token) hex
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  user_agent   TEXT NOT NULL DEFAULT '',
  ip           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE login_attempts (                 -- для лимита попыток входа
  key        TEXT NOT NULL,                   -- 'ip:<ip>' или 'email:<email>'
  at         INTEGER NOT NULL
);
CREATE INDEX login_attempts_key ON login_attempts(key, at);

CREATE TABLE workers (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  token_hash   TEXT NOT NULL UNIQUE,          -- sha256(token) hex
  capabilities TEXT NOT NULL DEFAULT '[]',    -- JSON-массив строк
  info         TEXT NOT NULL DEFAULT '{}',    -- версии codex/claude, ОС, имя ПК
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER
);

CREATE TABLE jobs (
  id               TEXT PRIMARY KEY,
  user_id          TEXT NOT NULL REFERENCES users(id),
  title            TEXT NOT NULL,             -- сначала первые слова запроса, после черновика — из SUMMARY.md
  prompt           TEXT NOT NULL,
  status           TEXT NOT NULL CHECK (status IN ('uploading','queued','running','needs_input','done','failed','canceled')),
  stage            TEXT CHECK (stage IN ('draft','verify','revise')),
  current_version  INTEGER NOT NULL DEFAULT 0, -- последняя выданная клиенту версия, 0 — ещё нет
  pending_revision INTEGER,                    -- номер версии, которую делает текущая доработка
  needs_attention  INTEGER NOT NULL DEFAULT 0,
  worker_id        TEXT REFERENCES workers(id),
  lease_epoch      INTEGER NOT NULL DEFAULT 0,
  lease_expires_at INTEGER,
  cancel_requested INTEGER NOT NULL DEFAULT 0,
  attempt          INTEGER NOT NULL DEFAULT 0, -- попытка текущего этапа: 0 — первая, 1 — авто-повтор
  question         TEXT,                       -- открытый вопрос агента клиенту
  state            TEXT NOT NULL DEFAULT '{}', -- JSON: codex_thread_id, claude_session_id, prompts_version, pending_answer
  error            TEXT,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  finished_at      INTEGER
);
CREATE INDEX jobs_user ON jobs(user_id, created_at DESC);
CREATE INDEX jobs_queue ON jobs(status, created_at);

CREATE TABLE files (                           -- всё, что лежит в blobs по заказу
  id          TEXT PRIMARY KEY,
  job_id      TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  kind        TEXT NOT NULL CHECK (kind IN ('input','draft','version','log','page','thumb','crop')),
  version     INTEGER NOT NULL DEFAULT 0,      -- для version — номер версии; для input — 0 (исходные) или номер доработки
  path        TEXT NOT NULL,                   -- относительный путь, разделитель '/'
  blob_key    TEXT NOT NULL UNIQUE,
  size        INTEGER NOT NULL,
  sha256      TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  UNIQUE (job_id, kind, version, path)
);

CREATE TABLE documents (                       -- состав комплекта из manifest.json, по снимку
  id           TEXT PRIMARY KEY,
  job_id       TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  snapshot     TEXT NOT NULL CHECK (snapshot IN ('draft','version')),
  version      INTEGER NOT NULL DEFAULT 0,     -- для draft — 0
  idx          INTEGER NOT NULL,               -- порядок в manifest
  title        TEXT NOT NULL,
  kind         TEXT NOT NULL,                  -- 'Word', 'Excel', 'КОМПАС-3D', 'PDF', 'Код', 'Презентация', …
  file_path    TEXT NOT NULL,                  -- путь в out/
  preview_path TEXT,                           -- путь PDF в preview/, если есть
  page_count   INTEGER NOT NULL DEFAULT 0,
  UNIQUE (job_id, snapshot, version, idx)
);

CREATE TABLE pages (
  document_id   TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  page          INTEGER NOT NULL,              -- с 1
  image_key     TEXT NOT NULL,                 -- PNG ~150 dpi
  thumb_key     TEXT NOT NULL,                 -- PNG ширина 240
  width         INTEGER NOT NULL,              -- пиксели image
  height        INTEGER NOT NULL,
  changed_boxes TEXT NOT NULL DEFAULT '[]',    -- JSON [{x,y,w,h}] в долях 0–1 относительно предыдущего снимка
  PRIMARY KEY (document_id, page)
);

CREATE TABLE revisions (
  id           TEXT PRIMARY KEY,
  job_id       TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  version      INTEGER NOT NULL,               -- какую версию создаёт (2, 3, …)
  comment      TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  completed_at INTEGER,
  UNIQUE (job_id, version)
);

CREATE TABLE revision_remarks (
  id          TEXT PRIMARY KEY,
  revision_id TEXT NOT NULL REFERENCES revisions(id) ON DELETE CASCADE,
  idx         INTEGER NOT NULL,                -- номер замечания 1, 2, …
  document_id TEXT NOT NULL REFERENCES documents(id),
  page        INTEGER NOT NULL,
  x REAL NOT NULL, y REAL NOT NULL, w REAL NOT NULL, h REAL NOT NULL,   -- доли 0–1, левый верхний угол + размер
  text        TEXT NOT NULL
);

CREATE TABLE agent_runs (
  id            TEXT PRIMARY KEY,
  job_id        TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  version       INTEGER NOT NULL,              -- для какой версии работали
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
  summary      TEXT NOT NULL,                  -- одна строка для списка, до 300 символов
  payload      TEXT NOT NULL DEFAULT '{}',     -- JSON, детали (команда и вывод до 20 КБ, путь файла, текст)
  PRIMARY KEY (agent_run_id, seq)
);

CREATE TABLE events (
  id                TEXT PRIMARY KEY,
  job_id            TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  ts                INTEGER NOT NULL,
  kind              TEXT NOT NULL,             -- см. 03-lifecycle.md
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
```

## Файлы (blobs)

Интерфейс `blobs.Blobs`: `Put(ctx, key, io.Reader) (size, sha256, error)`, `Open(ctx, key) (io.ReadSeekCloser, info, error)`, `Delete`, `List(prefix)`. Реализация `fs`: корень `data/blobs/`, ключ = относительный путь; запись во временный файл + `rename` (атомарно); ключ проверяется — никаких `..`, абсолютных путей, `\`.

Ключи:

```
jobs/<job_id>/input/<path>                       исходные файлы
jobs/<job_id>/input/revision-<n>/<path>          файлы, приложенные к доработке n
jobs/<job_id>/draft/out/<path>                   снимок черновика (после codex)
jobs/<job_id>/v<n>/out/<path>                    комплект версии n
jobs/<job_id>/<draft|v<n>>/pages/<document_idx>/<page>.png
jobs/<job_id>/<draft|v<n>>/thumbs/<document_idx>/<page>.png
jobs/<job_id>/input/revision-<n>/remarks/<idx>.png   вырезки выделенных мест
jobs/<job_id>/logs/<agent_run_id>.jsonl          сырой лог агента
jobs/<job_id>/v<n>/bundle.zip                    архив версии (создаётся при первом запросе, кешируется)
```

Пути файлов клиента нормализуются: разделитель `/`, без `..`, без ведущего `/`, без управляющих символов, имя ≤ 255 байт, путь ≤ 1024 байт. Повторный путь в заказе — заменяет файл.
