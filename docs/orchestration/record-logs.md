# Запись настоящих логов codex и claude (для владельца, ПК с Windows)

Нужно для тестов парсеров воркера (`internal/worker/agents/testdata/`). Задания выдуманные. Работать в пустой папке **вне** `Documents`. Команды запускать из `cmd.exe`, а не из PowerShell: PowerShell 5 портит кодировку при `<` и `>`.

1. Подготовка:

   ```bat
   mkdir C:\rec\job & cd /d C:\rec\job
   ```

   В Блокноте создать файлы в UTF-8 (`p1.txt` … `p4.txt`) с текстами:
   - `p1.txt`: Напиши calc.py для расчёта однопролётной балки (пролёт 6 м, равномерная нагрузка 12 кН/м): максимальный момент и прогиб. Запусти его и сохрани результат с таблицей в out/Расчёт.docx. Затем выполни python -c "print('x'*6000000)" и сообщи длину вывода.
   - `p2.txt`: Добавь в документ график прогиба.
   - `p3.txt`: Проверь out/Расчёт.docx и calc.py, открой картинку графика, найди и исправь ошибки.
   - `p4.txt`: Сделай заголовок таблицы жирным.

2. codex, новая сессия:

   ```bat
   codex exec --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox -C C:\rec\job - < p1.txt > codex_new.jsonl 2> codex_new.stderr.txt
   ```

3. codex, продолжение. `<thread_id>` взять из первой строки `codex_new.jsonl`:

   ```bat
   codex exec resume --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox <thread_id> - < p2.txt > codex_resume.jsonl
   ```

4. claude, новая сессия. uuid получить в PowerShell командой `[guid]::NewGuid()`:

   ```bat
   claude -p --output-format stream-json --verbose --dangerously-skip-permissions --session-id <uuid> < p3.txt > claude_new.jsonl
   ```

5. claude, продолжение той же сессии:

   ```bat
   claude -p --output-format stream-json --verbose --dangerously-skip-permissions --resume <uuid> < p4.txt > claude_resume.jsonl
   ```

6. claude, ошибка (несуществующая сессия):

   ```bat
   claude -p --output-format stream-json --verbose --dangerously-skip-permissions --resume 00000000-0000-4000-8000-000000000000 < p4.txt > claude_error.jsonl
   ```

Файлы `*.jsonl` передать агенту-разработчику. Он заменяет `C:\rec\job` и любые пути с именем пользователя на `C:\work\job`, проверяет отсутствие личных данных и кладёт файлы в `internal/worker/agents/testdata/`. Парсеры должны проходить на всех этих логах.
