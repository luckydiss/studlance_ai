# 01. Репозиторий, сборка, запуск

## Структура

```
api/openapi.yaml                 контракт API (источник правды для Go и TS)
cmd/server/main.go               studlance-server: serve | migrate | user create | worker token | backup
cmd/worker/main.go               studlance-worker: serve | probe
cmd/fakeagent/main.go            фейковые codex и claude для тестов (один бинарник, режим по argv[0] или флагу)
internal/config/                 конфиг сервера (флаги + env) и воркера (worker.toml)
internal/store/                  интерфейс Store, реализация sqlite/, миграции migrations/*.sql (goose), запросы queries/*.sql (sqlc)
internal/blobs/                  интерфейс Blobs, реализация fs/ (папка на диске)
internal/auth/                   Argon2id, сессии, cookie, middleware, лимит попыток
internal/jobs/                   бизнес-логика заказов, версий, доработок, вопросов (без HTTP)
internal/queue/                  выдача заказа, lease, heartbeat, fencing, авто-повтор, таймауты
internal/live/                   SSE-хаб в памяти
internal/httpapi/                HTTP-обработчики (сгенерированный интерфейс oapi-codegen + реализация)
internal/web/                    отдача собранных фронтов (go:embed dist) и /demo/*
internal/worker/                 главный цикл воркера, рабочая папка, этапы
internal/worker/agents/          запуск codex и claude, разбор JSONL, usage
internal/worker/procwin/         Windows Job Objects, закрытие КОМПАСа и Office (на других ОС — заглушка)
internal/worker/preview/         PDF → PNG (pdftoppm), мини-копии, сравнение страниц
internal/worker/client/          HTTP-клиент к /api/worker
prompts/                         draft.md, verify.md, revise.md, answer.md (go:embed в воркер)
web/                             pnpm workspace
web/shared/                      тема, компоненты, API-клиент, авторизация, SSE
web/client/                      кабинет (собирается в internal/web/dist/client)
web/admin/                       пульт (собирается в internal/web/dist/admin)
scripts/build.ps1                сборка на Windows
Makefile                         сборка и проверки на Linux/CI
.github/workflows/ci.yml         CI
docs/                            архитектура, дизайн, спеки
```

## Инструменты и библиотеки

| Что | Выбор |
|---|---|
| Go | 1.24 |
| HTTP | `net/http` (маршрутизация `ServeMux` Go 1.22+), код из `oapi-codegen` (strict server, std-http) |
| SQLite | `modernc.org/sqlite` (без CGO), режим WAL, `busy_timeout=5000`, `foreign_keys=on` |
| SQL | `sqlc` (engine sqlite), миграции `pressly/goose` (вшиты через `embed`) |
| Пароли | `golang.org/x/crypto/argon2` (Argon2id: time=2, memory=64 МБ, threads=2, salt 16 байт, key 32 байта, формат PHC-строки) |
| Логи | `log/slog`, JSON в stdout и файл `data/logs/server.log` |
| TOML | `github.com/pelletier/go-toml/v2` |
| Windows | `golang.org/x/sys/windows` |
| Фронт | Node 22, pnpm 9, React 18, Vite 5, TypeScript 5 (strict), React Router 6, TanStack Query 5, `openapi-typescript` + `openapi-fetch`, Biome |
| Стили | обычный CSS (CSS-модули) с токенами из DESIGN.md; без Tailwind и UI-китов — вёрстка повторяет макеты |
| Тесты | `go test` (+ `-race`), Vitest, Playwright |
| Линтеры | `golangci-lint` (дефолтный набор + `gofumpt`), Biome |

## Конфигурация

**Сервер** — флаги, у каждого есть переменная окружения:

| Флаг | Env | По умолчанию | Что |
|---|---|---|---|
| `--addr` | `STUDLANCE_ADDR` | `127.0.0.1:8080` | адрес HTTP |
| `--data` | `STUDLANCE_DATA` | `./data` | папка данных |
| `--session-ttl` | `STUDLANCE_SESSION_TTL` | `720h` | срок сессии |
| `--cookie-secure` | `STUDLANCE_COOKIE_SECURE` | `false` | флаг Secure у cookie |
| `--stage-timeout` | `STUDLANCE_STAGE_TIMEOUT` | `3h` | таймаут этапа |
| `--max-upload` | `STUDLANCE_MAX_UPLOAD` | `2GB` | лимит суммарного размера файлов заказа |

Папка данных: `data/studlance.db`, `data/blobs/…` (см. [02-data.md](02-data.md)), `data/demo/` (картинки для `/`), `data/logs/`.

**Воркер** — `worker.toml` рядом с exe (путь можно задать `--config`):

```toml
server_url = "http://127.0.0.1:8080"
token = "…"                       # выдаёт studlance-server worker token --name pc-1
name = "pc-1"
work_dir = 'C:\studlance\jobs'
pdftoppm = 'C:\studlance\poppler\bin\pdftoppm.exe'

[codex]
command = "codex"
args = []                          # доп. аргументы, например ["--model", "<модель>"]

[claude]
command = "claude"
args = []

[timeouts]
stage = "3h"
heartbeat = "15s"

[capabilities]
extra = []                         # добавить вручную
disable = []                       # убрать найденные
```

## Команды

```
studlance-server serve
studlance-server migrate
studlance-server user create --email a@b.ru --role admin|client --name "Имя"   # пароль спрашивает в консоли или --password-stdin
studlance-server worker token --name pc-1                                       # печатает токен один раз
studlance-server backup --out backup-2026-10-04.zip                             # SQLite (через VACUUM INTO) + data/blobs

studlance-worker serve [--config worker.toml]
studlance-worker probe                                                          # печатает найденные возможности и версии codex/claude
```

`serve` сервера применяет миграции при старте. Оба процесса корректно завершаются по Ctrl+C (воркер — см. [05-worker.md](05-worker.md)).

## Сборка

- `pnpm -C web install && pnpm -C web build` → `internal/web/dist/client/` и `internal/web/dist/admin/`. В репозитории лежит заглушка `internal/web/dist/.keep` и `index.html` «Фронт не собран», чтобы `go build` работал без Node.
- `go build -o bin/ ./cmd/...`; под Windows: `GOOS=windows GOARCH=amd64 go build -o bin/ ./cmd/server ./cmd/worker`.
- `scripts/build.ps1` делает обе части на Windows, `Makefile` — на Linux: цели `generate` (oapi-codegen, sqlc, openapi-typescript), `lint`, `test`, `web`, `build`, `build-windows`.
- Сгенерированный код коммитится; CI проверяет, что `make generate` не даёт диффа.

## CI (GitHub Actions)

На каждый PR: `golangci-lint`, `go test -race ./...`, `make generate` без диффа, `pnpm -C web lint test build`, Playwright e2e на Linux (сервер + воркер + фейковые агенты), артефакт с `studlance-server.exe` и `studlance-worker.exe`.

Поставляемый Windows-артефакт собирается с обоими production-фронтами: frozen install и web build выполняются **до** Go build в том же checkout либо собранные assets явно переносятся туда из другого job. Заглушка «Фронт не собран» допустима только для разработки без Node. Smoke-проверка поставляемого exe подтверждает `/healthz`, настоящие приложения `/` и `/admin`, доступность их JS/CSS и SPA deep links; отдельный успешный Web job сам по себе этого не доказывает.

Windows-скрипт сборки тоже использует `pnpm install --frozen-lockfile`. `backup` включает всю серверную БД (пользователей, сессии, заказы, состояние и хэши токенов) и `blobs`; локальные рабочие папки из `work_dir`, `worker.toml`, `demo` и CLI-профили сохраняются отдельно и приватно. Исходный токен воркера выдаётся только один раз: из БД его восстановить нельзя, повторная команда с тем же именем не перевыпускает токен. Для продолжения закреплённых заказов нужен прежний worker ID и его конфиг; создание другого воркера не переносит эти заказы.

## Запуск на ПК (инструкция для владельца — оформить в `docs/INSTALL.md`)

Для чистой Windows-установки инструкция даёт конкретный воспроизводимый путь получения Poppler: источник готового Windows-архива с версиями/зависимостями и проверкой происхождения/хэша либо проверенную пошаговую сборку. Ссылка только на исходники Poppler и предложение владельцу самостоятельно найти Windows-сборку недостаточны. Проверка включает реальный `pdftoppm` на выдуманном PDF и получение PNG, а не только `--version`.

В заданиях автозапуска сервера и воркера нужно явно отключить ограничение длительности выполнения: у Планировщика Windows по умолчанию `ExecutionTimeLimit = PT72H`. В UI снять «Останавливать задачу, выполняемую дольше…»; эквивалент `PT0S` означает без ограничения. Описать проверку этих настроек для обоих заданий и влияние условий питания. Настройка выполняется владельцем, не агентом во время разработки/ревью.

1. Создать учётную запись Windows `studlance` без личных файлов; под ней установить и залогинить `codex` и `claude`, установить КОМПАС-3D, Office, Python, Playwright с Chromium, poppler.
2. Положить `studlance-server.exe` в `C:\studlance\server\`, запустить `studlance-server.exe user create …` для админа и клиента, затем `studlance-server.exe serve`.
3. Скопировать картинки в `C:\studlance\server\data\demo\` по таблице из [design/README.md](../design/README.md).
4. `studlance-server.exe worker token --name pc-1` → вписать токен в `worker.toml`; под учёткой `studlance` запустить `studlance-worker.exe serve`.
5. Открыть `http://localhost:8080` (кабинет) и `http://localhost:8080/admin` (пульт).
