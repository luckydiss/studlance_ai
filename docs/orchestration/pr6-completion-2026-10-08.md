# PR 6 — завершение пульта, 2026-10-08

Статус: PR #6 принят и влит в `main` по отдельной команде владельца. Проверенный head `2f05fc835b88d92ae69c725f2c61541d2616c0c8`; merge-коммит `8e040495b79f69041606f9b4b0a2d372785f72e4`.

Незавершённая реализация сохранена и продолжена в изолированном checkout. Исходные изменения не потеряны. Реализованы маршруты пульта, заказы и фильтры, разбор заказа, история и live-события, документы и замечания, действия администратора, клиенты и воркеры.

## Архитектурные решения

- Admin GET исходника проверяет роль, заказ и точную запись `kind=input`, отдаёт сохранённый blob потоково. Номер доработки берётся из `files.version`, а не имени файла. Контракт и сгенерированные Go/TypeScript обновлены.
- Переход из пульта на общий вход после текущего 401 переносит `reauth=1`: это только указание показать форму, без изменения серверной авторизации. Успешный `/me` не отменяет повторный вход. Возврат администратора в пульт выполняется полной навигацией между SPA.
- Автоматический повтор admin query исключает 401/403/404 и ошибки прежней сессии. Текущий 401 сразу завершает локальную сессию; единственный повтор разрешён для сети и 5xx. Обнаруженный браузерным тестом дефект с поглощением разового 401 исправлен и проверен targeted и полным браузерными прогонами.
- Загрузка истории сериализована. При смене набора запусков ожидающий добор выполняется из `finally`, в том числе после отмены предыдущего эффекта; прежние callbacks не записывают данные новой карточки.
- Длинная история сохраняется в памяти, отображаемый список ограничен 200 строками; HTTP-курсор отделён от live-пачек.

## Проверки на текущем дереве

Независимые ревью API и интерфейса проведены. Найденная гонка истории исправлена и проверена браузерными тестами. Первый CI Web e2e на исходном head поймал ненадёжный тестовый маршрут no-draft fixture; маршрут переведён на context-level matcher, а тест теперь проверяет фактический ответ браузерного GET. Targeted и полный локальный повтор на исправленном дереве прошли, все CI jobs на финальном head завершились успешно.

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
| Playwright | полный прогон `--workers=1` после фикса fixture: 44 pass (25 PR5 + 19 PR6), 0 fail, 0 skip; 12.3 min |
| 2 000 шагов | 2 004 шага, 5 HTTP страниц, последняя seq 2 004, DOM ограничен 200 строками, фильтр 23 ms |
| 390 px | нет горизонтального переполнения; проверено, что trace заканчивается до начала SheetsPanel, внутренний список прокручивается отдельно |
| CI на исходном head | Web, Generate, Windows build, Go и Go e2e прошли; Web e2e упал только на старой проверке `noDraftFixtureServed` |
| CI на финальном head | все 6 checks прошли: Web, Generate, Windows build, Go, Go e2e, Web e2e |
| Скриншоты | synthetic admin review сохранён в 390 px и 1440 px; mockup оставлен без изменений |

Локальные Go-прогоны выполнены на Go 1.26.8 с `CGO_ENABLED=0`, без race detector. Проверка `-race` подтверждена зелёными Go/Go e2e jobs CI принятого head. Raw логи, исходный снимок и локальные пути хранятся вне публичного репозитория. Данные для браузерных сценариев синтетические.

## Follow-up findings and verification

Browser testing found and fixed one product defect: an admin detail request returned a one-off 401, then React Query retried and got 200, so the cache error handler never expired the session. Admin queries now retry only network errors and 5xx (once); stale-session errors and 401/403/404 do not retry. An independent reviewer confirmed the policy, and the targeted login test passed.

A second race covered late responses from a prior card. Every route instance carries an active view token; admin detail requests, action mutations, notes, cache updates and visible errors check the captured token and session before writing. Note drafts reset on route change. Independent source review found no P1/P2 in these paths. The late-note regression (held 201 response A→B) and late-detail regression (held, completed 200 and held 401 after navigating to B) both passed.

The combined order-filter test passes with the exact client id, email query, failed status and checked attention filter; it asserts only the target job remains and its URL filters survive opening/back. Email search permits persistent synthetic Stranger orders from earlier tests in the shared harness DB; a unique unsubmitted title verifies exact one-row search behavior without relying on the database being empty.

At narrow widths, the desktop panel flex-grow values had caused the trace to shrink while the sheets panel occupied the same vertical space. The ≤1120 px layout now gives each panel intrinsic height and caps the trace steps scroller at 420 px. The 390 px browser regression compares the trace/sheets bounding boxes and verifies that the step list scrolls within its own panel. The final screenshot shows SheetsPanel below the complete trace panel.

Raw logs and screenshots are stored outside the repository in the review artifact directory. They include web lint/unit/build, post-build Go vet/unit, the complete 44-test Playwright run, targeted race regressions, and 390/1440 screenshots. After the browser-route fixture update, the targeted test passed 1/1 and the clean full Playwright run `pnpm exec playwright test --workers=1` passed 44/44 with no skips. The route update is test-only; production code and the final web build are unchanged. All six GitHub CI checks passed on code/test head `2158df2` and on the final documentation head `2f05fc8`. Local Go checks used Go 1.26.8 with CGO disabled; race detector was not run locally.

## Слияние по команде владельца — 2026-10-08

Перед слиянием повторно получены удалённые ветки: база `40ab2669c9ef399c0142d9aa397021827f724c13`, head PR `2f05fc835b88d92ae69c725f2c61541d2616c0c8`. На этом точном head все шесть CI checks завершены успешно. Независимый ревьюер подтвердил отсутствие P1/P2; финальные коммиты после реализации изменяют тестовую fixture и отчёт.

В отдельном checkout создан локальный merge-коммит `8e040495b79f69041606f9b4b0a2d372785f72e4` с этими двумя родителями, конфликтов нет. До документальных изменений его полное tracked-дерево совпадало с принятым head. После frozen install в web и нового production web build повторно прошли `go vet ./...` и `go test -count=1 ./...` с exit 0; оба Windows amd64 exe также собраны с exit 0. Первоначальный install из корня без package.json не считается успешной проверкой; корректный frozen install в web выполнен полностью. Локально CGO выключен; новый race-прогон не заявляется. Полный Playwright повторно не запускался: production-дерево совпадает с принятым, 44/44 и CI сохраняют силу.

Оркестратор отдельно обновил состояние и подготовил [промпт PR 7](pr7-prompt.md). Спеки 01/09 уточнены: поставляемый Windows-артефакт должен включать оба production-фронта и проходить smoke именно этого exe. Текущий Windows CI job собирает Go без web build, поэтому до исправления в PR 7 его артефакт не считается готовой поставкой сайта. Функциональность PR 6 проверена с собранными фронтами. Production-код оркестратор не менял; частные данные, demo и raw-артефакты в main не включены.
