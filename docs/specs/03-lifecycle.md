# 03. Жизненный цикл заказа

## Поля, которые задают состояние

`status` (`uploading | queued | running | needs_input | done | failed | canceled`), `stage` (`draft | verify | revise`), `current_version`, `pending_revision`, `attempt`, `question`, `cancel_requested`, `worker_id`, `lease_epoch`, `lease_expires_at`.

## Переходы

| Из | Событие | В | Кто | Что ещё меняется |
|---|---|---|---|---|
| — | создание заказа | `uploading` | клиент | `title` = первые 8 слов запроса (обрезка до 80 символов) |
| `uploading` | `submit` (есть хотя бы один файл или запрос ≥ 20 символов) | `queued` | клиент | `stage = draft` |
| `queued` | выдача воркеру (claim) | `running` | воркер | `worker_id`, `lease_epoch += 1`, lease 60 с |
| `running` | heartbeat | `running` | воркер | lease продлевается до now + 60 с |
| `running` | этап `draft` закончен успешно | `running` | воркер | `stage = verify`, `attempt = 0`, снимок черновика, `title` из `SUMMARY.md` |
| `running` | этап `verify` закончен успешно | `done` | воркер | `current_version = 1`, `stage = NULL`, `finished_at` |
| `running` | этап `revise` закончен успешно | `done` | воркер | `current_version = pending_revision`, `pending_revision = NULL`, `revisions.completed_at` |
| `running` | агент задал вопрос (`QUESTIONS.md`) | `needs_input` | воркер | `question` = текст; этап остаётся прежним |
| `needs_input` | ответ клиента или админа | `queued` | клиент/админ | `question = NULL`, `state.pending_answer` = ответ |
| `running` | этап упал / таймаут, `attempt = 0` | `queued` | воркер/сервер | `attempt = 1` (авто-повтор того же этапа на том же воркере) |
| `running` | этап упал / таймаут, `attempt = 1` | `failed` | воркер/сервер | `needs_attention = 1`, `error` |
| `failed` | «Повторить» | `queued` | админ | `attempt = 0`, `error = NULL`, этап прежний |
| `done` | доработка | `queued` | клиент | `stage = revise`, `pending_revision = current_version + 1`, запись в `revisions` и `revision_remarks`, `needs_attention = 1` |
| `uploading`, `queued`, `needs_input` | отмена | `canceled` | клиент/админ | сразу |
| `running` | отмена | `running` → `canceled` | клиент/админ | `cancel_requested = 1`; воркер видит флаг в ответе heartbeat, останавливает агента, вызывает finish с `outcome = canceled` → `canceled` |

Нельзя (ответ `409 conflict`): доработка не из `done`; ответ не из `needs_input`; отмена из `done`, `failed`, `canceled`; повтор не из `failed`.

**Заказ закреплён за воркером:** после первой выдачи `worker_id` не меняется (там рабочая папка и сессии агентов). Если lease истёк, тот же воркер заберёт заказ снова (`lease_epoch += 1`) и продолжит этап. Воркер делает один заказ за раз, поэтому `claim` от воркера значит, что он свободен: его заказы в `running` выдаются ему первыми как `continue`, даже если lease ещё не истёк (перезапуск воркера быстрее 60 с). Фоновая проверка сервера раз в минуту: `running` с lease, истёкшим больше 10 минут назад, получает `needs_attention = 1` (клиенту ничего не меняется). Таймаут этапа (по умолчанию 3 ч) считается только по времени работы: от начала этапа, а после ответа на вопрос — от выдачи с `action: answer`; ожидание ответа в `needs_input` не считается. Превышение сервер засчитывает как сбой этапа.

**Запрошенная отмена важнее сбоя:** если `cancel_requested = 1`, то сбой этапа, таймаут или вопрос агента переводят заказ в `canceled`, а не в авто-повтор, `failed` или `needs_input`.

**Новая попытка этапа** (`start` или `revise`) начинает снимок этапа с чистого листа: незафиксированные файлы, страницы и документы снимка от прошлой попытки удаляются, чтобы в архив версии не попали старые файлы.

## Что видит клиент

Клиенту отдаётся только `client_status` и окошко статуса; этапы, агенты, проверка — не показываются.

| Состояние | `client_status` | Текст статуса |
|---|---|---|
| `uploading` | `uploading` | Загрузка файлов |
| `queued`, `current_version = 0` | `accepted` | Заказ принят |
| `running`, `stage` = `draft` или `verify` | `in_progress` | Выполняем |
| `queued` или `running`, `stage = revise` | `revising` | Дорабатываем |
| `needs_input` | `needs_input` | Нужно уточнение |
| `done` | `done` | Готово |
| `failed` | `delayed` | Задерживается — мы уже разбираемся |
| `canceled` | `canceled` | Отменён |

Повтор после сбоя (`queued`, `attempt = 1`) клиенту показывается как прежний статус («Выполняем» / «Дорабатываем»).

**Окошко статуса** (сворачиваемое, внизу справа на странице заказа) — шаги простыми словами:

| Шаг | Начинается, когда |
|---|---|
| Заказ принят | `status` ≠ `uploading` |
| Разбираем задание и методичку | воркер начал этап `draft` (событие `stage_started` с `stage = draft`) |
| Делаем работу | прошла ≥ 1 мин после начала `draft` или пришёл первый шаг трейса codex |
| Оформляем по требованиям методички | начался этап `verify` |
| Готово — версия N | `done` |

В таблице — когда шаг **начинается**. Последний начавшийся шаг — текущий (синяя точка), все до него — готовы, после него — будущие. Пока заказ ждёт воркера, текущий шаг — «Заказ принят».

Для доработки шаги: «Получили замечания» → «Дорабатываем» → «Готово — версия N». Вопрос агента показывается в окошке и на странице заказа (см. [07-web-client.md](07-web-client.md)).

## События (`events`)

| `kind` | Клиенту | `data` |
|---|---|---|
| `created` | да | — |
| `submitted` | да | `{files, bytes}` |
| `claimed` | нет | `{worker, epoch}` |
| `stage_started` | нет | `{stage, attempt}` |
| `draft_ready` | нет | `{documents}` |
| `question` | да | `{text}` |
| `answered` | да | `{by: client\|admin}` |
| `version_ready` | да | `{version}` |
| `revision_requested` | да | `{version, remarks}` |
| `stage_failed` | нет | `{stage, attempt, error}` |
| `retried` | нет | `{by: auto\|admin}` |
| `failed` | да (как «Задерживается») | `{error}` |
| `cancel_requested` | да | — |
| `canceled` | да | — |
| `note` | нет | `{note_id}` |

Каждое событие публикуется в SSE-хаб (см. [04-api.md](04-api.md#live)).
