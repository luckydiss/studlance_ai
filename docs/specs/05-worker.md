# 05. Воркер

`studlance-worker.exe serve` — консольная программа на ПК с Windows. Забирает заказы у сервера, выполняет этапы с помощью codex и claude, отдаёт трейс, файлы и итог. Один заказ за раз. Всё общение с сервером — исходящие HTTP-запросы к `/api/worker` ([04-api.md](04-api.md#воркер--apiworker)).

## Главный цикл

```
register (capabilities, info) → цикл:
  claim (long-poll 25 с) → 204: повторить; 200 Assignment:
    подготовить папку заказа
    запустить heartbeat (каждые 15 с)
    выполнить действие (start | continue | answer | revise) для этапа
    остановить heartbeat
```

- Сеть недоступна → повторять с паузой 2, 4, 8 … до 60 с; работающий агент не трогать.
- Любой ответ `409 stale_lease` → немедленно остановить агента (см. «Остановка»), бросить заказ, вернуться к claim.
- Ответ heartbeat с `cancel: true` → остановить агента, `finish {outcome: canceled}`.
- Ctrl+C → остановить агента, **не** вызывать finish (заказ продолжится после перезапуска как `continue`), выйти.

## Рабочая папка

`<work_dir>\<job_id>\`, контракт агентов — [ARCHITECTURE.md §4](../ARCHITECTURE.md#рабочая-папка). Воркер:

1. Создаёт папку, скачивает `input/` (сверяя sha256, существующие одинаковые файлы не качает заново). Файлы доработки — в `input/revision-<n>/`.
2. Пишет `TASK.md` (при первой подготовке):
   ```markdown
   # Задание клиента
   <prompt>

   # Приложенные файлы
   - input/задание.pdf (1.2 МБ)
   - …
   ```
3. Служебное — в `.studlance\`: `state.json` (job_id, этап, версия, id сессий, номер последнего вопроса), `logs\`, `snapshots\draft\`, `snapshots\v<n>\` (копии `out/`, `preview/`, PNG страниц — нужны для сравнения версий и вырезок).

## Этапы

| Этап | Агент | Сессия | Успех, когда |
|---|---|---|---|
| `draft` | codex | новая (`codex exec`), `thread_id` из события `thread.started` → `POST /state` | код выхода 0, есть валидный `manifest.json` (≥1 документ, файлы существуют в `out/`) |
| `verify` | claude | новая, `--session-id <uuid v4>` генерирует воркер → `POST /state` до запуска | код выхода 0, `result.is_error = false`, валидный `manifest.json` |
| `revise` | claude | та же, что в `verify` (`--resume <uuid>`) | как `verify` |

Команды запуска — [ARCHITECTURE.md, «Запуск CLI»](../ARCHITECTURE.md#запуск-cli) плюс `args` из конфига. Промпт всегда в **stdin** (UTF-8), рабочая папка = cwd. Тексты промптов — [06-prompts.md](06-prompts.md).

### Действия

- **start** — этап с нуля. Для `attempt = 1` (авто-повтор) — новая сессия, в промпт добавляется блок «Предыдущая попытка не завершилась…» (см. 06).
- **continue** — после перезапуска воркера или истёкшего lease: если id сессии этапа известен — `resume` с промптом `continue`; если нет — как `start`.
- **answer** — `resume` сессии текущего этапа с промптом `answer` (ответ клиента).
- **revise** — подготовка доработки (ниже) → `resume` сессии claude с промптом `revise`.

### После этапа

| Итог | Действия воркера |
|---|---|
| вопрос | В папке появился непустой `QUESTIONS.md` (создан или изменён во время этапа) → перенести его в `.studlance\questions\q-<n>.md`, `POST /question {text}`. **Не** вызывать finish. |
| `draft` успешен | снимок `draft` (ниже) → `commit` (с `title` из первой строки `SUMMARY.md`, без `#`, до 120 символов) → сразу этап `verify` (новый `POST /runs`), не возвращаясь к claim |
| `verify` успешен | снимок `v1` (сравнение с `draft`) → `commit` (+ `verification.json`) → `finish {ok, version: 1}` |
| `revise` успешен | снимок `v<n>` (сравнение с `v<n-1>`) → `commit` → `finish {ok, version: n}` |
| ошибка / таймаут | загрузить лог, `PATCH run {outcome, error}` → `finish {outcome: failed\|timeout, error}`; сервер сам решает: авто-повтор или `failed` |

Ошибка — понятная строка для админа: «codex завершился с кодом 1», «нет manifest.json», «manifest.json: файл out/… не найден», «превышен таймаут этапа 3 ч» и т. п.

### Подготовка доработки

1. Скачать файлы `input/revision-<n>/`.
2. Для каждого замечания вырезать область из PNG страницы снимка `v<n-1>` (с полями 3 % от размера листа, не выходя за края), сохранить `input/revision-<n>/remarks/<idx>.png`, загрузить на сервер.
3. Записать `REVISION-<n>.md`:
   ```markdown
   # Доработка — версия <n>
   Комментарий клиента: <comment или «—»>

   ## Замечания на листах
   1. «<текст>» — <document_title> (<file_path>), стр. <page>, область: x=<x>, y=<y>, w=<w>, h=<h> (доли листа, от левого верхнего угла). Вырезка: input/revision-<n>/remarks/1.png
   …

   ## Приложенные файлы
   - input/revision-<n>/…
   ```

## Снимок версии

1. Скопировать `out/` и `preview/` в `.studlance\snapshots\<draft|v<n>>\`.
2. Загрузить на сервер каждый файл из `out/` (`PUT …/snapshot/<s>/files?path=…`).
3. Прочитать `manifest.json`; для каждого документа получить PDF превью:
   - `preview` указан и файл есть — его;
   - иначе конвертировать сам: `.docx .doc .rtf .odt` — Word (COM), `.xlsx .xls` — Excel (COM, `ExportAsFixedFormat`), `.pptx` — PowerPoint (COM), `.pdf` — как есть, `.png .jpg` — одна «страница» без PDF; остальное (`.cdw`, код) — без превью, `page_count = 0`. Конвертация через PowerShell-скрипт, таймаут 3 мин на файл.
4. PDF → PNG: `pdftoppm -r 110 -png <pdf> <dir>\p` → страницы; мини-копии шириной 240 px (Go, `golang.org/x/image/draw`, CatmullRom). Загрузить `pages/<idx>/<page>.png` и `thumbs/<idx>/<page>.png`.
5. Сравнение с предыдущим снимком (для `v1` — с `draft`, для `v<n>` — с `v<n-1>`), документ сопоставляется по `file_path`, страница по номеру:
   - обе картинки уменьшить до ширины 400, в оттенки серого; сетка 40×N клеток; клетка «изменилась», если средняя абсолютная разница > 6/255 или страницы нет в предыдущем снимке;
   - соседние изменившиеся клетки объединить в прямоугольники (связные области), отдать как `changed_boxes` в долях 0–1; больше 30 прямоугольников на странице → одна рамка на всю страницу.
6. `POST …/snapshot/<s>/commit` с документами, страницами и (для версий) содержимым `verification.json`.

`manifest.json` пишут агенты:

```json
{ "title": "…",
  "documents": [ { "file": "out/Пояснительная записка.docx", "title": "Пояснительная записка", "kind": "Word", "preview": "preview/Пояснительная записка.pdf" } ] }
```

Валидация: `documents` непустой; `file` начинается с `out/`, файл существует; `preview` (если есть) начинается с `preview/`; `kind` — свободная строка (рекомендуемые: Word, Excel, КОМПАС-3D, PDF, Презентация, Код, Изображение). Невалидный манифест на этапе = ошибка этапа.

`verification.json` (claude): `{ "found": int, "fixed": int, "remaining": [ { "severity": "major|minor", "description": "…" } ] }`. Отсутствие или невалидность — не ошибка, просто `null` в пульте.

## Запуск агента и трейс

1. `POST /runs` → `run_id`. Перед запуском `claude` — `POST /state {claude_session_id}`.
2. Запустить процесс: cwd — папка заказа, stdin — промпт, stdout — JSONL построчно, stderr — в тот же сырой лог с пометкой `{"_stderr": "…"}`.
3. Каждую строку: дописать в `.studlance\logs\<run_id>.jsonl`; разобрать в 0…n шагов трейса; копить и отправлять пачками (раз в 1 с или по 100 шагов), `seq` — сквозной номер с 1.
4. По завершении: `PATCH run` (exit_code, outcome, токены, стоимость, error), `PUT …/log` (сырой лог).

### Разбор codex (`codex exec --json`)

| Событие | Шаг / действие |
|---|---|
| `thread.started {thread_id}` | `POST /state {codex_thread_id}` |
| `item.completed`, `item.type = agent_message` | `message`: первые 300 символов `text` |
| `item.completed`, `reasoning` | `reasoning`: первые 300 символов |
| `item.completed`, `command_execution` | `command`: `command` (до 300) + payload `{command, exit_code, output: последние 20 КБ aggregated_output}` |
| `item.completed`, `file_change` | `file`: «изменены файлы: a, b» + payload `{changes}` |
| `item.completed`, `web_search` | `web`: `query` |
| `item.completed`, `mcp_tool_call` | `tool`: `server/tool` |
| `turn.completed {usage}` | сложить `input_tokens` (+`cached_input_tokens` отдельно в payload), `output_tokens` |
| `turn.failed`, `error` | `error`: сообщение |
| прочее | игнор (остаётся в сыром логе) |

### Разбор claude (`--output-format stream-json --verbose`)

| Событие | Шаг / действие |
|---|---|
| `system`, `subtype = init` | проверить, что `session_id` совпадает с ожидаемым |
| `assistant`, блок `text` | `message` |
| `assistant`, блок `thinking` | `reasoning` |
| `assistant`, блок `tool_use` `Bash` | `command`: `input.command` |
| `assistant`, `tool_use` `Write` / `Edit` / `MultiEdit` / `NotebookEdit` | `file`: путь |
| `assistant`, `tool_use` `WebSearch` / `WebFetch` | `web`: запрос / URL |
| `assistant`, прочие `tool_use` | `tool`: имя |
| `user`, `tool_result` | дописать результат (последние 20 КБ) в payload шага с тем же `tool_use_id`; `is_error` → шаг `error` |
| `result` | `is_error`, `total_cost_usd`, `usage` → `PATCH run`; `subtype ≠ success` → этап неуспешен |

Неизвестные события и поля не ломают разбор. Тесты на записанных логах обоих CLI обязательны (`internal/worker/agents/testdata/`).

## Остановка агента (Windows)

- Процесс агента сразу после старта помещается в **Job Object** с `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`; все дочерние процессы попадают туда же. Остановка = `TerminateJobObject`.
- Перед этапом запомнить PID запущенных `KOMPAS.exe`, `kStudy.exe`, `WINWORD.EXE`, `EXCEL.EXE`, `POWERPNT.EXE`, `soffice.bin`; после этапа (любого исхода) завершить такие процессы, которых не было до этапа (их запускает COM вне дерева агента).
- Таймаут этапа (конфиг, по умолчанию 3 ч от старта этапа) → остановка → `finish {timeout}`.
- На не-Windows (тесты, CI) — обычное убийство группы процессов; закрытие Office — заглушка.

## Возможности (`probe` и `register`)

`codex`, `claude` — команда из конфига отвечает на `--version` (версии — в `info`); `kompas` — в реестре есть ProgID `KOMPAS.Application.7` или `Kompas.Application.5`; `word`, `excel`, `powerpoint` — ProgID `Word.Application`, `Excel.Application`, `PowerPoint.Application`; `python` — `python --version`; `libreoffice` — `soffice --version`; плюс `extra`, минус `disable` из конфига. В MVP требования заказа пустые — возможности только показываются в пульте.

## Фейковые агенты (`cmd/fakeagent`)

Запускаются вместо `codex` и `claude` в тестах (через конфиг `command`). Поведение задаётся переменной окружения `FAKEAGENT_SCRIPT` (JSON) или содержимым `TASK.md` (строки-маркеры):

| Маркер в запросе | Поведение |
|---|---|
| — | нормальная работа: несколько шагов JSONL нужного CLI, `out/записка.docx` (минимальный валидный docx), `out/расчёт.xlsx`, `preview/*.pdf` (2–3 страницы, сгенерированы), `manifest.json`, `SUMMARY.md`, у claude — `VERIFICATION.md`, `verification.json`, изменённая страница |
| `#ask` | на этапе `draft` один раз пишет `QUESTIONS.md` и выходит с 0 |
| `#fail-draft`, `#fail-verify` | выход с кодом 1 на этом этапе (каждая попытка) |
| `#fail-once` | падает только на первой попытке этапа `draft` |
| `#hang` | спит, пока не убьют |
| `#no-pdf` | не кладёт `preview/` (воркер должен сконвертировать или оставить без превью) |

Формат JSONL — как у настоящих CLI (по таблицам выше), чтобы парсеры проверялись тем же путём.
