# PR 6 — завершение пульта, 2026-10-08

Статус: локальная реализация и приёмочные проверки завершены; PR опубликован, CI ожидается. Слияние требует отдельной команды владельца.

Незавершённая реализация сохранена и продолжена в изолированном checkout. Исходные изменения не потеряны. Реализованы маршруты пульта, заказы и фильтры, разбор заказа, история и live-события, документы и замечания, действия администратора, клиенты и воркеры.

## Архитектурные решения

- Admin GET исходника проверяет роль, заказ и точную запись `kind=input`, отдаёт сохранённый blob потоково. Номер доработки берётся из `files.version`, а не имени файла. Контракт и сгенерированные Go/TypeScript обновлены.
- Переход из пульта на общий вход после текущего 401 переносит `reauth=1`: это только указание показать форму, без изменения серверной авторизации. Успешный `/me` не отменяет повторный вход. Возврат администратора в пульт выполняется полной навигацией между SPA.
- Автоматический повтор admin query исключает 401/403/404 и ошибки прежней сессии. Текущий 401 сразу завершает локальную сессию; единственный повтор разрешён для сети и 5xx. Обнаруженный браузерным тестом дефект с поглощением разового 401 исправлен и независимо проверен по коду; повторный браузерный прогон ожидается.
- Загрузка истории сериализована. При смене набора запусков ожидающий добор выполняется из `finally`, в том числе после отмены предыдущего эффекта; прежние callbacks не записывают данные новой карточки.
- Длинная история сохраняется в памяти, отображаемый список ограничен 200 строками; HTTP-курсор отделён от live-пачек.

## Проверки на текущем дереве

Независимые ревью API и интерфейса проведены. Найденная гонка истории исправлена и повторно проверена по коду. Финальные браузерные доказательства ещё ожидаются.

| Проверка | Результат |
|---|---|
| Frozen install | exit 0 |
| Web lint | exit 0 на финальном дереве, включая проверку границ панелей при 390 px |
| Web unit | 38 pass: 16 shared + 22 client; admin `no tests` не учитывается |
| Web build | exit 0: оба TypeScript/Vite приложения, после последнего CSS изменения |
| Go vet | exit 0 после финального web build |
| Go unit | `go test -count=1 ./...` exit 0 после финального web build; все пакеты прошли |
| Go e2e с Poppler | 12 pass, 0 fail, 0 skip |
| Генерация Go и TypeScript | два запуска, файлы не изменились |
| Playwright | полный прогон `--workers=1`: 44 pass (25 PR5 + 19 PR6), 0 fail, 0 skip |
| 2 000 шагов | 2 004 шага, 5 HTTP страниц, последняя seq 2 004, DOM ограничен 200 строками, фильтр 26 ms |
| 390 px | нет горизонтального переполнения; проверено, что trace заканчивается до начала SheetsPanel, внутренний список прокручивается отдельно |
| CI | ожидается после публикации PR |
| Скриншоты | synthetic admin review сохранён в 390 px и 1440 px; mockup оставлен без изменений |

Локальные Go-прогоны выполнены на Go 1.26.8 с `CGO_ENABLED=0`, без race detector. Проверка `-race` должна быть подтверждена CI. Raw логи, исходный снимок и локальные пути хранятся вне публичного репозитория. Данные для браузерных сценариев синтетические.

## Follow-up findings and verification

Browser testing found and fixed one product defect: an admin detail request returned a one-off 401, then React Query retried and got 200, so the cache error handler never expired the session. Admin queries now retry only network errors and 5xx (once); stale-session errors and 401/403/404 do not retry. An independent reviewer confirmed the policy, and the targeted login test passed.

A second race covered late responses from a prior card. Every route instance carries an active view token; admin detail requests, action mutations, notes, cache updates and visible errors check the captured token and session before writing. Note drafts reset on route change. Independent source review found no P1/P2 in these paths. The late-note regression (held 201 response A→B) and late-detail regression (held, completed 200 and held 401 after navigating to B) both passed.

The combined order-filter test passes with the exact client id, email query, failed status and checked attention filter; it asserts only the target job remains and its URL filters survive opening/back. Email search permits persistent synthetic Stranger orders from earlier tests in the shared harness DB; a unique unsubmitted title verifies exact one-row search behavior without relying on the database being empty.

At narrow widths, the desktop panel flex-grow values had caused the trace to shrink while the sheets panel occupied the same vertical space. The ≤1120 px layout now gives each panel intrinsic height and caps the trace steps scroller at 420 px. The 390 px browser regression compares the trace/sheets bounding boxes and verifies that the step list scrolls within its own panel. The final screenshot shows SheetsPanel below the complete trace panel.

Raw logs and screenshots are stored outside the repository in the review artifact directory. They include web lint/unit/build, post-build Go vet/unit, the complete 44-test Playwright run, targeted race regressions, and 390/1440 screenshots. The full Playwright run was `pnpm exec playwright test --workers=1` with 44 pass, 0 fail, 0 skip. Go checks used Go 1.26.8 with CGO disabled; race detector and Windows CI remain to be confirmed by CI. No production code changed after the final build and test run.
