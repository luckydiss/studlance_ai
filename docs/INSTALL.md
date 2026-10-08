# Установка и обновление Studlance на одном ПК с Windows

Инструкция рассчитана на локальный MVP: один Windows-ПК, `studlance-server.exe`, `studlance-worker.exe`, SQLite и файлы на диске. Кабинет и пульт встроены в сервер. Браузер открывает тот же адрес, на который подключается воркер. Это не инструкция для Docker, фоновой службы, сетевого сервера или публичного сайта.

## Что нужно и когда

| Инструмент | Нужен для | Проверка |
|---|---|---|
| `studlance-server.exe`, `studlance-worker.exe` | Постоянная работа | Сервер отвечает на `/healthz`, воркер отображается online |
| Codex CLI и Claude Code CLI | Реальная работа воркера | `codex --version`, `claude --version`; вход в CLI выполнен именно под Windows-пользователем `studlance` |
| Office с Word, Excel и PowerPoint; КОМПАС-3D | Создание и просмотр соответствующих файлов и PDF превью | Открыть каждую программу и активировать под `studlance`; `worker probe` показывает обнаруженные COM ProgID |
| Python и пакеты `pandas`, `matplotlib`, `python-docx`, `openpyxl` | Скрипты и документы из текущих промптов | `python --version`, `python -m pip show pandas matplotlib python-docx openpyxl` |
| Playwright для Python и Chromium | Скриншоты работающих программ в заданиях с программой | `python -m playwright --version`; Chromium установлен для `studlance` |
| Poppler `pdftoppm.exe` | Страницы и миниатюры из PDF | `pdftoppm -v` и полный путь `Get-Command pdftoppm` |
| Go **1.24.0**, Node.js **22**, pnpm **9.15.0** | Только сборка из исходников | `go version`, `node --version`, `pnpm --version` |

При получении готового комплекта сборки Go, Node.js и pnpm на рабочем ПК не нужны. На нём всё равно нужны CLI и приложения, требуемые типом реальной работы. Python-пакеты и Playwright/Chromium входят в текущий общий промпт; для новой задачи смотрите её реальные требования, не устанавливайте случайные зависимости заранее.

## Установите приложения под отдельной Windows-учётной записью

1. В Windows создайте отдельную локальную учётную запись `studlance` с обычными правами. Используйте её для рабочего профиля CLI, браузерных данных, файлов заданий и запуска воркера. Не храните пароль Windows в скрипте или аргументах команды.
2. Установите и запустите CLI под `studlance`, затем выполните интерактивный вход в каждый сервис. Следуйте актуальным официальным инструкциям [Codex CLI](https://developers.openai.com/codex/cli) и [Claude Code](https://code.claude.com/docs/en/getting-started). Для Claude Code используйте поддерживаемый Windows-путь из официальной инструкции. Не переносите CLI-сессии из другого пользователя.
3. Установите лицензированные Office и КОМПАС-3D из официального источника/кабинета лицензирования. Для Office следуйте инструкциям Microsoft по [установке](https://support.microsoft.com/en-us/office/lifecycle/install-office-apps-from-office-365) и [активации](https://support.microsoft.com/en-us/microsoft-365-activation-licensing/office-install/activate-office-for-windows). Для КОМПАС-3D используйте [официальную страницу АСКОН/КОМПАС](https://kompas.ru/kompas-3d/v25/) и выберите подходящую лицензию. Войдите/активируйте программы под `studlance` и один раз откройте Word, Excel, PowerPoint и КОМПАС-3D в интерактивном рабочем столе.
4. Установите Python из [официальных сборок Python для Windows](https://www.python.org/downloads/windows/). В PowerShell, открытом именно как `studlance`, проверьте, что `Get-Command python` указывает на ожидаемый исполняемый файл, затем установите используемые общим промптом пакеты:

   ```powershell
   python -m pip install pandas matplotlib python-docx openpyxl playwright
   if ($LASTEXITCODE -ne 0) { throw "Установка Python-пакетов завершилась ошибкой" }
   python -m playwright install chromium
   if ($LASTEXITCODE -ne 0) { throw "Установка Chromium для Playwright завершилась ошибкой" }
   ```

   Официальные инструкции: [Playwright для Python](https://playwright.dev/python/docs/library) и [установка браузеров](https://playwright.dev/python/docs/browsers). Устанавливайте Chromium в профиль `studlance`, потому что кэш браузера пользовательский.
5. Установите Poppler из выбранного проверенного источника Windows-сборки. [Официальный сайт Poppler](https://poppler.freedesktop.org/) публикует исходные релизы и подписи, но не предлагает готовый официальный Windows-инсталлятор. Не используйте выдуманное имя пакета или случайный неподписанный установщик. Проверьте происхождение и подпись/хэш выбранного архива, распакуйте его в `C:\studlance\poppler`, найдите `pdftoppm.exe` и закрепите именно этот полный путь в конфиге.
6. Проверьте команды и версии в PowerShell пользователя `studlance`. Для каждой внешней команды проверяйте `$LASTEXITCODE` сразу после её запуска:

   ```powershell
   Get-Command codex,claude,python,pdftoppm | Select-Object Name,Source
   codex --version
   if ($LASTEXITCODE -ne 0) { throw "Codex CLI не запустился" }
   claude --version
   if ($LASTEXITCODE -ne 0) { throw "Claude Code не запустился" }
   python --version
   if ($LASTEXITCODE -ne 0) { throw "Python не запустился" }
   python -m pip show pandas matplotlib python-docx openpyxl playwright
   if ($LASTEXITCODE -ne 0) { throw "Не найдены Python-пакеты" }
   python -m playwright --version
   if ($LASTEXITCODE -ne 0) { throw "Playwright не запустился" }
   pdftoppm -v
   if ($LASTEXITCODE -ne 0) { throw "pdftoppm не запустился" }
   ```

   Откройте новый PowerShell после установки PATH. Убедитесь, что `Get-Command` показывает ожидаемые пути, а не другой одноимённый CLI или Python.

## Получите комплект

Выберите ровно один вариант.

### Готовые Windows exe

Скачайте Windows artifact из успешного GitHub Actions workflow для конкретного commit SHA, который выбран для установки. Сверьте SHA workflow run с SHA релиза/PR и сохраните его в журнале установки. Не ставьте artifact из «последнего успешного запуска» без сверки commit: в нём может быть другая версия. В архиве должны быть ровно `studlance-server.exe` и `studlance-worker.exe`; CI для этого набора сначала собирает оба production фронтенда и встраивает их в серверный exe, затем проверяет smoke.

### Сборка из исходников

Для сборки нужны Git, Go `1.24.0` (значение `go` из `go.mod`), Node.js 22 и pnpm `9.15.0` (зафиксирован в `web/package.json`). Установите Node 22 с [официальной страницы Node.js](https://nodejs.org/en/download/archive/v22.22.0); установите pnpm по [официальной инструкции](https://pnpm.io/installation) и проверьте версии. Путь `/Documents` используйте только для чтения: исходники и `bin` разместите вне него.

```powershell
go version
if ($LASTEXITCODE -ne 0) { throw "Не найден Go" }
node --version
if ($LASTEXITCODE -ne 0) { throw "Не найден Node.js" }
pnpm --version
if ($LASTEXITCODE -ne 0) { throw "Не найден pnpm" }
```

Получите нужный проверенный SHA в новый каталог вне Documents, перейдите в корень репозитория и выполните:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1
if ($LASTEXITCODE -ne 0) { throw "Сборка или packaged smoke завершились ошибкой" }
```

Скрипт использует `pnpm install --frozen-lockfile`, собирает оба фронтенда, затем Windows x64 сервер и воркер, после чего запускает smoke на этих exe. Ожидаемый результат — сообщения `Windows package smoke passed` и `Complete Windows package built and smoke-tested`; готовые файлы лежат в `bin`. Не собирайте exe простым `go build` без предшествующего web build: получится сервер с заглушкой вместо SPA.

## Создайте постоянные папки и положите файлы

Откройте PowerShell пользователя `studlance`. Для примера используются только общие пути:

```powershell
$Root = 'C:\studlance'
$ServerDir = Join-Path $Root 'server'
$WorkerDir = Join-Path $Root 'worker'
$DataDir = Join-Path $ServerDir 'data'
$JobsDir = Join-Path $Root 'jobs'
$BackupDir = Join-Path $Root 'backups'
$PopplerDir = Join-Path $Root 'poppler'
New-Item -ItemType Directory -Force -Path $ServerDir,$WorkerDir,$DataDir,$JobsDir,$BackupDir,$PopplerDir | Out-Null
```

Скопируйте `studlance-server.exe` в `C:\studlance\server`, `studlance-worker.exe` в `C:\studlance\worker`, а проверенный `pdftoppm.exe` и нужные ему файлы Poppler — в постоянную папку `C:\studlance\poppler`. В `C:\studlance\worker` скопируйте пример `worker.example.toml` как `worker.toml`, затем отредактируйте локальную копию. Оставьте `token = ""` до шага выдачи токена. В примере уже есть один комплект секций; не добавляйте дублирующие `[codex]`, `[claude]` и другие TOML-секции.

Оставьте постоянными имя и путь `work_dir`, например `C:\studlance\jobs`. Подпапка `<work_dir>\<job_id>` содержит состояние, идентификаторы CLI-сессий, логи и снимки. Не очищайте её при обновлении. Путь Poppler задайте абсолютным, например `C:\studlance\poppler\bin\pdftoppm.exe`. TOML-пути с пробелами заключайте в одинарные кавычки; все Windows-команды с путями ниже передают каждый путь как отдельный заключённый в кавычки аргумент.

Поле `server_url` в `worker.toml` должно быть `http://127.0.0.1:8080`. Именно этот origin используйте в браузере. Не смешивайте `localhost`, IP-адрес, HTTP/HTTPS или разные порты: сессия браузера и проверки origin привязаны к точному адресу.

## Проверка возможностей и токен воркера

Сначала проверьте локальный профиль под `studlance`:

```powershell
$WorkerExe = 'C:\studlance\worker\studlance-worker.exe'
$WorkerConfig = 'C:\studlance\worker\worker.toml'
Get-Command codex,claude,python,pdftoppm | Select-Object Name,Source
& $WorkerExe probe --config $WorkerConfig
if ($LASTEXITCODE -ne 0) { throw "worker probe завершился ошибкой" }
```

`probe` показывает доступные версии CLI, Python и обнаруженные Windows COM ProgID. Это только проверка наличия команд/регистраций: она не доказывает, что CLI авторизован, Office/КОМПАС реально открывают документ через COM или что заказ завершится. Авторизацию проверяет интерактивный вход в CLI под `studlance`; работу COM и рендеринг — боевая приёмка ниже.

Создайте базу и администратора. `--data` всегда указывает один абсолютный путь — добавляйте его к каждой команде сервера, чтобы смена текущего каталога PowerShell не создала вторую базу. Для новых паролей бинарник спрашивает пароль без отображения символов; не передавайте его через аргумент, переменную окружения, скрипт или лог:

В примере ниже адреса `.test` выдуманы. До применения владелец заменяет их на выбранные локальные логины; приложение не отправляет по этим адресам письма.

```powershell
$ServerExe = 'C:\studlance\server\studlance-server.exe'
$DataDir = 'C:\studlance\server\data'
& $ServerExe migrate --data $DataDir
if ($LASTEXITCODE -ne 0) { throw "migrate завершился ошибкой" }
& $ServerExe user create --email 'admin@example.test' --role admin --name 'Администратор' --data $DataDir
if ($LASTEXITCODE -ne 0) { throw "создание admin завершилось ошибкой" }
& $ServerExe user create --email 'client@example.test' --role client --name 'Тестовый клиент' --data $DataDir
if ($LASTEXITCODE -ne 0) { throw "создание client завершилось ошибкой" }
```

Создайте постоянную серверную запись воркера. Команда выдаёт секретный token один раз. Скопируйте его непосредственно в локальный `C:\studlance\worker\worker.toml`, заменив пустое значение `token`; не помещайте значение в переписку, transcript, issue или PR. Сервер хранит хэш token, исходный token из копии базы восстановить нельзя. Повторный `worker token --name pc-1` с тем же именем завершится ошибкой, а новое имя создаст новый worker ID и не заберёт заказы, закреплённые за старым.

```powershell
& $ServerExe worker token --name 'pc-1' --data $DataDir
if ($LASTEXITCODE -ne 0) { throw "выдача worker token завершилась ошибкой" }
```

Ограничьте доступ к локальному `worker.toml` и сделайте защищённую отдельную копию конфига с token. Для smoke с fakeagent создавайте второй тестовый конфиг вне репозитория; не меняйте основной профиль установки и не запускайте fakeagent с настоящими пользовательскими заданиями.

`timeouts.stage` задаёт предел одного этапа (по умолчанию 3 часа), а `timeouts.heartbeat` задаёт период heartbeat/продления lease (по умолчанию 15 секунд). Значения должны соответствовать рабочему ПК и серверному `--stage-timeout`. Не уменьшайте таймаут только ради теста реальных заданий. Сохраняйте неизменными `name`, token и `work_dir` при перезапуске и обновлении. CLI запускается командами `codex` и `claude` из PATH либо абсолютным путём. `args` — только дополнительные аргументы к реальному CLI; протокольные параметры воркера добавляются отдельно. Не прописывайте в публичном конфиге аккаунты, секреты или идентификаторы моделей.

## Запуск и первая проверка

Откройте первое окно PowerShell пользователя `studlance` и оставьте его работать:

```powershell
$ServerExe = 'C:\studlance\server\studlance-server.exe'
$DataDir = 'C:\studlance\server\data'
& $ServerExe serve --data $DataDir --addr '127.0.0.1:8080'
if ($LASTEXITCODE -ne 0) { throw "Сервер завершился с ошибкой" }
```

Откройте второе окно PowerShell под той же учётной записью:

```powershell
$WorkerExe = 'C:\studlance\worker\studlance-worker.exe'
$WorkerConfig = 'C:\studlance\worker\worker.toml'
& $WorkerExe serve --config $WorkerConfig
if ($LASTEXITCODE -ne 0) { throw "Воркер завершился с ошибкой" }
```

В третьем окне PowerShell проверьте здоровье сервера:

```powershell
$health = Invoke-RestMethod 'http://127.0.0.1:8080/healthz'
if (-not $health.ok) { throw "Сервер не готов" }
$health
```

Ожидаемый JSON содержит `ok: true`. В браузере того же ПК откройте `http://127.0.0.1:8080/`, войдите тестовым client, затем перейдите на `/admin` и войдите admin. В пульте администратор должен видеть воркер online. Пустая главная страница без картинок `/demo` — штатное поведение до ручного заполнения; страницы сайта и вход при этом работают.

## Необязательные изображения главной

Владелец вручную копирует нужные картинки из приватного `studlance_ai_private/demo/` в `<data>\demo`. Имена, исходники, целевые ширины и требование JPEG приведены в таблице [docs/design/README.md — «Картинки для `/`»](design/README.md). Не скачивайте приватные материалы, не коммитьте их и не помещайте их в публичные artifacts. При отсутствии файлов сайт показывает пустые листы того же размера; это предусмотренное поведение.

## Автозапуск в интерактивной сессии

Office и КОМПАС используются через COM. Запускайте сервер и воркер в интерактивной сессии `studlance` после входа этого пользователя в Windows; не настраивайте их как Windows-службу и не рассчитывайте на работу COM до входа пользователя. После перезагрузки требуется войти в Windows как `studlance`.

Владелец настраивает Планировщик заданий через его UI (`taskschd.msc`):

1. Создайте задание `Studlance server`. На вкладке **Общие** выберите пользователя `studlance` и **Выполнять только для вошедшего пользователя**. На вкладке **Триггеры** добавьте **При входе в систему** этого пользователя.
2. На вкладке **Действия** укажите программу `C:\studlance\server\studlance-server.exe`, аргументы `serve --data "C:\studlance\server\data" --addr "127.0.0.1:8080"`, поле **Начать в** — `C:\studlance\server`.
3. В **Параметрах** выберите для уже выполняющегося задания **Не запускать новый экземпляр**. Включите перезапуск при сбое, например раз в минуту не более трёх раз. Не задавайте пароль в аргументах или скрипте.
4. Аналогично создайте `Studlance worker` для `studlance`, **Выполнять только для вошедшего пользователя**, с триггером при входе в систему и задержкой запуска 1 минута. Программа: `C:\studlance\worker\studlance-worker.exe`; аргументы: `serve --config "C:\studlance\worker\worker.toml"`; поле **Начать в**: `C:\studlance\worker`. В параметрах также установите **Не запускать новый экземпляр** и восстановление после сбоя.
5. Перезайдите под `studlance`, проверьте `/healthz`, затем online воркер в `/admin`. Если сервер ещё не готов, воркер сам повторяет сетевое подключение с увеличивающейся задержкой. На обычном запуске одновременно не запускайте ручной экземпляр и плановое задание.

При остановке выключите оба задания в Планировщике, чтобы они не стартовали повторно. Для штатной остановки работающего воркера нажмите `Ctrl+C`, дождитесь выхода, затем нажмите `Ctrl+C` в окне сервера. Не завершайте все процессы Office или КОМПАС по имени: другие окна и несохранённые документы могут принадлежать пользователю. Если применяется собственный PowerShell-helper для фонового запуска, задавайте ему скрытое окно (`-WindowStyle Hidden`); не добавляйте пароль в helper.

## Обновление без потери состояния

1. В Планировщике отключите задания автозапуска. Остановите воркер через `Ctrl+C`, затем сервер через `Ctrl+C`.
2. На остановленных процессах создайте резервную копию в постоянную папку. ZIP содержит всю `studlance.db` — пользователей, сессии/хэши токенов, заказы и состояния — и `blobs`. База и файлы заказов приватны, храните архив отдельно с ограниченным доступом.

   ```powershell
   $ServerExe = 'C:\studlance\server\studlance-server.exe'
   $DataDir = 'C:\studlance\server\data'
   $Backup = 'C:\studlance\backups\studlance-before-update.zip'
   & $ServerExe backup --out $Backup --data $DataDir
   if ($LASTEXITCODE -ne 0) { throw "backup завершился ошибкой" }
   ```

3. Дополнительно сохраните целиком `C:\studlance\jobs`, `C:\studlance\worker\worker.toml`, `<data>\demo` (если владелец копировал туда картинки) и профиль Windows-пользователя `studlance`. CLI auth/session stores находятся в профиле этой учётной записи. Не сбрасывайте в ней авторизацию Codex/Claude и не создавайте новую Windows-учётку для продолжения работ.
4. Замените только `studlance-server.exe` и `studlance-worker.exe` в их постоянных каталогах. Не удаляйте data, `worker.toml`, `jobs` или профиль. Сохраните SHA комплекта.
5. Запустите сервер с прежними абсолютными `--data` и адресом, дождитесь `/healthz`; затем запустите воркер с прежним абсолютным `--config`. Включите задания автозапуска после проверки входа и online воркера.

При миграции схемы автоматического отката базы нет. Если нужно вернуть старые бинарники, используйте совместимую копию базы и файлов в новой пустой папке данных; не распаковывайте старую базу поверх живого SQLite/WAL.

После `Ctrl+C` текущий `running` заказ не завершается как ошибка. Тот же worker ID может получить его как `continue` сразу после перезапуска, ещё до истечения lease; если сессия CLI известна, воркер возобновляет её. Продолжение зависит от сохранённых `.studlance` состояний, CLI auth/session stores того же профиля, исходных файлов и ответа CLI. Таймаут этапа продолжает считаться от сохранённого времени старта; перезапуск не обнуляет его и не обещает бесконечное возобновление.

## Ручное восстановление ZIP в новую папку

У команды `backup` нет команды `restore`. Восстанавливайте в отдельный новый пустой каталог только при остановленных сервере и воркере. Не распаковывайте архив поверх существующей/живой базы.

```powershell
$Backup = 'C:\studlance\backups\studlance-before-update.zip'
$RestoreData = 'C:\studlance\restore-data-20261008'
if (Test-Path -LiteralPath $RestoreData) { throw "Выберите новый пустой каталог данных" }
New-Item -ItemType Directory -Path $RestoreData | Out-Null
Expand-Archive -LiteralPath $Backup -DestinationPath $RestoreData
if (-not (Test-Path -LiteralPath (Join-Path $RestoreData 'studlance.db') -PathType Leaf)) { throw "В архиве нет studlance.db" }
if (-not (Test-Path -LiteralPath (Join-Path $RestoreData 'blobs') -PathType Container)) { throw "В архиве нет blobs" }
```

Поднимите совместимый `studlance-server.exe` с `--data $RestoreData` и тем же loopback-адресом; `serve` применит миграции. Проверьте `/healthz`, вход существующим тестовым пользователем и доступность ранее сохранённого файла. Для проверки байтов сравните SHA-256 скачанного до резервирования файла с файлом после восстановления (`Get-FileHash -Algorithm SHA256`). Затем остановите сервер через `Ctrl+C`. Верните рабочую конфигурацию `--data` только после проверки; для восстановления работы заказа отдельно верните прежние `jobs`, `worker.toml` и профиль `studlance`.

## Состав резервирования

| Данные | Где хранятся | В `backup --out` |
|---|---|---|
| SQLite: пользователи, хэши паролей/token, сессии, заказы и их состояние | `<data>\studlance.db` | Да |
| Файлы заказов | `<data>\blobs` | Да |
| Изображения `/demo` | `<data>\demo` | Нет, копировать отдельно |
| Работа CLI, `state.json`, логи и снимки | `work_dir\<job_id>`, включая `.studlance` | Нет, сохранять весь `work_dir` |
| Worker token в открытом виде | `worker.toml` | Нет; хранится отдельно под защитой |
| Сессии авторизации CLI | Профиль Windows `studlance` | Нет; сохранять профиль отдельно |

ZIP, `worker.toml`, профиль пользователя и файлы работ — приватные данные. Ограничьте права на каталог и внешнюю копию архива. Не добавляйте архивы и реальные задания в GitHub, bug reports или публичные логи.
