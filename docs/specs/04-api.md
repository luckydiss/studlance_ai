# 04. API

Первым делом агент пишет `api/openapi.yaml` (OpenAPI 3.1) строго по этому документу; Go-сервер и TS-клиент генерируются из него. Здесь — нормативное описание.

## Общее

- Префикс `/api`. JSON UTF-8. Время — RFC 3339 UTC. Идентификаторы — строки.
- Ошибка — всегда `{"error": {"code": "…", "message": "…"}}`, `message` по-русски для показа клиенту.

| HTTP | `code` | Когда |
|---|---|---|
| 400 | `bad_request` | неверное тело или параметры |
| 401 | `unauthorized` | нет сессии / неверный токен воркера |
| 403 | `forbidden` | не та роль; `Origin` не совпал |
| 404 | `not_found` | нет объекта **или он чужой** (неразличимо) |
| 409 | `conflict` | недопустимый переход статуса; устаревший `epoch` (`code: stale_lease`) |
| 413 | `too_large` | превышен лимит загрузки |
| 429 | `rate_limited` | лимит попыток входа |
| 500 | `internal` | всё остальное (подробности только в логе) |

- **Сессия:** cookie `sl_session` (случайные 32 байта base64url), `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` по конфигу. В БД — только sha256. Продление `last_seen_at` не чаще раза в минуту.
- **CSRF:** все не-GET запросы к `/api/auth`, `/api/client`, `/api/admin` проверяют, что `Origin` совпадает с хостом сервера; иначе 403.
- **Лимит входа:** не более 10 неудачных попыток за 15 минут на IP и на email → 429.
- Объекты `Job` для клиента и для админа — разные схемы (у клиента нет этапов, трейса, воркера).

## Auth — `/api/auth`

| Метод | Путь | Тело / ответ |
|---|---|---|
| POST | `/login` | `{email, password}` → `200 {user}` + cookie; неверно → 401 «Неверная почта или пароль» |
| POST | `/logout` | → `204`, сессия удалена, cookie очищена |
| GET | `/me` | → `200 {user}` или 401 |

`user`: `{id, email, name, role}`.

## Клиент — `/api/client` (роль `client`, только свои заказы)

### Схемы

```jsonc
// JobSummary
{ "id": "…", "title": "Курсовая работа, вариант 14…", "client_status": "done",
  "status_text": "Готово", "current_version": 2, "created_at": "…", "updated_at": "…" }

// JobDetail = JobSummary +
{ "prompt": "…",
  "question": "…" | null,                       // открытый вопрос агента
  "status_steps": [ {"title": "Заказ принят", "state": "done|active|pending"} … ],   // окошко статуса (03-lifecycle.md)
  "input_files": [ {"path": "задание.pdf", "size": 123} … ],
  "versions": [ { "version": 1, "ready_at": "…", "documents": [Document…] } … ],      // по возрастанию
  "planned_documents": [ {"title": "Пояснительная записка", "kind": "Word"} … ],      // из черновика, пока v1 не готова; иначе []
  "revisions": [ { "version": 2, "comment": "…", "created_at": "…", "completed_at": "…" | null,
                   "remarks": [Remark…] } … ],
  "can_cancel": true, "can_revise": false, "can_answer": false }

// Document
{ "id": "…", "idx": 0, "title": "Пояснительная записка", "kind": "Word",
  "file_path": "Пояснительная записка.docx", "size": 123456, "page_count": 32,
  "download_url": "/api/client/jobs/{id}/versions/1/files/Пояснительная%20записка.docx" }

// Page
{ "page": 1, "width": 1240, "height": 1754,
  "image_url": "…/pages/{document_id}/1.png", "thumb_url": "…/thumbs/{document_id}/1.png",
  "changed_boxes": [ {"x":0.1,"y":0.2,"w":0.3,"h":0.05} ] }

// Remark
{ "idx": 1, "document_id": "…", "page": 16, "x": 0.06, "y": 0.6, "w": 0.88, "h": 0.11, "text": "Добавьте в таблицу время переходного процесса" }
```

### Эндпоинты

| Метод | Путь | Что |
|---|---|---|
| GET | `/jobs` | `{jobs: JobSummary[]}` новые сверху |
| POST | `/jobs` | `{prompt}` (1–20 000 символов) → `201 JobDetail` со статусом `uploading` |
| PUT | `/jobs/{id}/input?path=<относительный путь>` | тело — байты файла (`application/octet-stream`), только в `uploading` → `200 {path, size}`; лимит суммарно `--max-upload` |
| DELETE | `/jobs/{id}/input?path=…` | удалить загруженный файл (только `uploading`) → 204 |
| POST | `/jobs/{id}/submit` | `uploading` → `queued` → `200 JobDetail` |
| GET | `/jobs/{id}` | `JobDetail` |
| POST | `/jobs/{id}/answer` | `{text}` (1–10 000) → `200 JobDetail` |
| POST | `/jobs/{id}/cancel` | → `200 JobDetail` |
| POST | `/jobs/{id}/revisions` | `multipart/form-data`: поле `data` = JSON `{comment, remarks: [{document_id, page, x, y, w, h, text}]}` (comment или хотя бы одно замечание обязательны; x, y, w, h в 0–1), поля `files[]` с `filename` = относительный путь → `201 JobDetail` |
| GET | `/jobs/{id}/versions/{v}/documents/{document_id}/pages` | `{pages: Page[]}` |
| GET | `/jobs/{id}/versions/{v}/pages/{document_id}/{page}` | картинка страницы; `{page}` — `16.png` |
| GET | `/jobs/{id}/versions/{v}/thumbs/{document_id}/{page}` | мини-копия; `{page}` — `16.png` |
| GET | `/jobs/{id}/versions/{v}/files/{path}` | файл комплекта; `{path}` — один сегмент, `/` кодируется как `%2F` (`Чертежи%2FЛист%201.pdf`); `Content-Disposition: attachment; filename*=UTF-8''…` |
| GET | `/jobs/{id}/versions/{v}/bundle.zip` | архив всех файлов `out/` версии (имена в UTF-8, флаг EFS) |
| GET | `/jobs/{id}/stream` | SSE, см. [Live](#live) |

Файлы и картинки отдаются с `Cache-Control: private, max-age=31536000, immutable` (версии не меняются) и поддержкой `Range`.

## Пульт — `/api/admin` (роль `admin`)

```jsonc
// AdminJobSummary
{ "id", "title", "client": {"id","email","name"}, "status", "stage", "client_status", "current_version",
  "pending_revision", "needs_attention", "attempt", "worker": "pc-1" | null, "created_at", "updated_at" }

// AdminJobDetail = AdminJobSummary + JobDetail (кроме client-only флагов) +
{ "error": "…" | null, "state": {…}, "lease_epoch": 3, "lease_expires_at": "…",
  "draft": { "documents": [Document…] } | null,         // снимок черновика codex
  "verification": { "found": 7, "fixed": 7, "remaining": [ {"severity","description"} ] } | null,  // по последней версии
  "agent_runs": [ { "id", "agent", "stage", "version", "attempt", "started_at", "finished_at", "outcome",
                    "input_tokens", "output_tokens", "cost_usd", "error" } ],
  "events": [ {"ts","kind","data"} ], "notes": [ {"id","author","text","created_at"} ] }
```

| Метод | Путь | Что |
|---|---|---|
| GET | `/jobs?status=&attention=1&q=` | `{jobs: AdminJobSummary[]}` |
| GET | `/jobs/{id}` | `AdminJobDetail` |
| GET | `/jobs/{id}/runs/{run_id}/steps?after_seq=` | `{steps: [{seq, ts, type, summary, payload}]}` до 500 за раз |
| GET | `/jobs/{id}/runs/{run_id}/log` | сырой JSONL |
| GET | `/jobs/{id}/draft/...` | те же ресурсы страниц, мини-копий и файлов, что у версий, но для черновика |
| GET | `/jobs/{id}/versions/{v}/...` | как у клиента, без проверки владельца |
| POST | `/jobs/{id}/answer` | как у клиента, `answered.by = admin` |
| POST | `/jobs/{id}/retry` | `failed` → `queued` |
| POST | `/jobs/{id}/cancel` | как у клиента |
| POST | `/jobs/{id}/attention` | `{needs_attention: false}` — снять флаг |
| POST | `/jobs/{id}/notes` | `{text}` → `201` |
| GET | `/workers` | `[{id, name, capabilities, info, last_seen_at, online, current_job}]`; `online` = last_seen < 45 с |
| GET | `/clients` | `[{id, email, name, jobs_total, last_job_at}]` |
| GET | `/jobs/{id}/stream` | SSE со всеми событиями и новыми шагами трейса |

## Воркер — `/api/worker`

Заголовок `Authorization: Bearer <token>`. Каждая запись по заказу содержит `epoch`; если он не равен `jobs.lease_epoch` или `worker_id` не этого воркера — `409 stale_lease`, воркер должен прекратить работу над заказом. Протокол и порядок вызовов — в [05-worker.md](05-worker.md).

| Метод | Путь | Тело → ответ |
|---|---|---|
| POST | `/register` | `{capabilities: [], info: {}}` → `200 {worker_id}`; обновляет `last_seen_at` |
| POST | `/claim` | `{}` → long-poll до 25 с: `200 Assignment` или `204` |
| POST | `/jobs/{id}/heartbeat` | `{epoch}` → `200 {lease_expires_at, cancel: bool}` |
| GET | `/jobs/{id}/input` | `?epoch=` → `{files: [{path, size, sha256, revision}]}` |
| GET | `/jobs/{id}/input/{path}` | `?epoch=` → байты; `{path}` — один сегмент с `%2F` |
| POST | `/jobs/{id}/runs` | `{epoch, agent, stage, version, attempt, session_id?}` → `201 {run_id}` |
| PATCH | `/jobs/{id}/runs/{run_id}` | `{epoch, session_id?, finished_at?, exit_code?, outcome?, input_tokens?, output_tokens?, cost_usd?, error?}` → 204 |
| POST | `/jobs/{id}/runs/{run_id}/steps` | `{epoch, steps: [{seq, ts, type, summary, payload}]}` → 204; дубликаты `(run_id, seq)` молча игнорируются |
| PUT | `/jobs/{id}/runs/{run_id}/log` | `?epoch=`, тело — JSONL → 204 |
| POST | `/jobs/{id}/state` | `{epoch, state: {codex_thread_id?, claude_session_id?, prompts_version?}}` → 204 (merge в `jobs.state`) |
| PUT | `/jobs/{id}/snapshot/{draft\|v<n>}/files?path=` | `?epoch=`, тело — байты файла из `out/` → 204 |
| PUT | `/jobs/{id}/snapshot/{draft\|v<n>}/pages/{document_idx}/{page}` и `/thumbs/…` (`{page}` — `16.png`) | `?epoch=` → 204 |
| PUT | `/jobs/{id}/input/revision/{n}/remarks/{idx}` | `?epoch=`, вырезка выделенного места → 204 |
| POST | `/jobs/{id}/snapshot/{draft\|v<n>}/commit` | `{epoch, title?, documents: [{idx, title, kind, file_path, preview_path?, page_count, pages: [{page, width, height, changed_boxes}]}], verification?}` → 204; фиксирует снимок, записывает `documents`/`pages`; для `draft` — `stage = verify`, `title`; для `v<n>` ничего не выдаёт клиенту (это делает `finish`) |
| POST | `/jobs/{id}/question` | `{epoch, text}` → 204, `needs_input` |
| POST | `/jobs/{id}/finish` | `{epoch, outcome: ok\|failed\|canceled\|timeout, version?, error?}` → 204 (см. переходы в [03-lifecycle.md](03-lifecycle.md)) |

`Assignment`:

```jsonc
{ "job_id": "…", "epoch": 4, "lease_expires_at": "…",
  "action": "start" | "continue" | "answer" | "revise",
  "stage": "draft" | "verify" | "revise", "attempt": 0,
  "prompt": "текст запроса клиента",
  "version": 2,                              // какую версию делает этап (1 для draft/verify)
  "state": { "codex_thread_id": "…", "claude_session_id": "…" },
  "answer": "…" | null,                      // для action=answer
  "revision": { "version": 2, "comment": "…",
                "remarks": [ {"idx":1, "document_title":"…", "file_path":"…", "page":16, "x":…, "y":…, "w":…, "h":…, "text":"…"} ],
                "files": [ {"path":"…"} ] } | null }
```

`action`: `start` — этап с нуля (первая выдача или авто-повтор с `attempt = 1`); `continue` — lease истёк или воркер перезапускался, продолжить тот же этап (`resume`); `answer` — клиент ответил на вопрос; `revise` — новая доработка.

## Live

`GET …/stream` — `text/event-stream`. При подключении первым приходит `event: snapshot` с полным `JobDetail` (`AdminJobDetail` для пульта). Далее при любом изменении заказа — `event: job` с новым `JobDetail`; для пульта ещё `event: steps` `{run_id, steps: […]}` с новыми шагами трейса. Каждые 20 с — комментарий `: ping`. Хаб в памяти: `live.Hub.Publish(jobID, event)`; подписки клиентов фильтруются по владельцу заказа.

## Статика

- `/` и все пути, кроме `/api`, `/admin`, `/demo` — SPA кабинета (`index.html` для неизвестных путей).
- `/admin/*` — SPA пульта (только роль `admin`; иначе редирект на `/login`).
- `/demo/<имя>` — файлы из `data/demo/` (только имена из таблицы в [design/README.md](../design/README.md)), `Cache-Control: public, max-age=86400`.
- `GET /healthz` → `200 {"ok":true}` (проверяет доступ к БД и папке файлов).
