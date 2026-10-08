package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// longTraceSteps is the size of the #longtrace draft trace: enough to force
// several HTTP pages of 500 steps and a bounded DOM in the panel.
const longTraceSteps = 2000

// runCodex imitates `codex exec --json`: the draft stage.
func runCodex(cfg *config) int {
	if cfg.markers["#hang"] {
		return hang()
	}
	dir, err := stateDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 2
	}

	threadID := cfg.sessionID
	if threadID == "" {
		threadID = "thread-" + randHex(8)
	}
	em := newEmitter(cfg.sleepMs)

	switch {
	case cfg.markers["#fail-draft"]:
		return codexFailure(em, threadID, "draft failed (#fail-draft marker)")
	case cfg.markers["#fail-once"] && !flagExists(dir, "failed-draft"):
		if err := setFlag(dir, "failed-draft"); err != nil {
			fmt.Fprintln(os.Stderr, "fakeagent:", err)
			return 2
		}
		return codexFailure(em, threadID, "draft failed on first attempt (#fail-once marker)")
	case cfg.markers["#ask"] && !flagExists(dir, "asked"):
		return codexAsk(em, dir, threadID)
	}

	em.emit(map[string]any{"type": "thread.started", "thread_id": threadID})

	if cfg.markers["#slow"] {
		time.Sleep(5 * time.Second)
	}

	if cfg.markers["#longtrace"] {
		// A long, complete trace: many JSONL steps so the panel's history
		// paging, filtering and bounded DOM can be exercised (08-web-admin.md).
		// Keep a short streaming cadence so the same real scenario can verify
		// both live SSE delivery and the completed HTTP history pages.
		em.delay = time.Millisecond
		for i := 1; i <= longTraceSteps; i++ {
			em.emit(codexItem(fmt.Sprintf("item_%d", i), "reasoning", map[string]any{
				"text": fmt.Sprintf("Шаг %d: проверяем элемент расчёта номер %d", i, i),
			}))
		}
		em.delay = time.Duration(lineDelayMs) * time.Millisecond
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 2
	}
	files, err := writeDraftArtifacts(cwd, !cfg.markers["#no-pdf"])
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 1
	}

	em.emit(codexItem("item_1", "reasoning", map[string]any{
		"text": "Planning the documents: an explanatory note and a calculation spreadsheet, then PDF previews.",
	}))
	if cfg.markers["#longline"] {
		// A single JSONL line with 10 MB of command output (long-line handling
		// in the worker's reader).
		em.emit(codexItem("item_big", "command_execution", map[string]any{
			"command":           "python3 -c \"print('x'*10000000)\"",
			"exit_code":         0,
			"aggregated_output": strings.Repeat("x", 10000000),
			"status":            "completed",
		}))
	}
	em.emit(codexItem("item_2", "command_execution", map[string]any{
		"command":           "python3 build_report.py --out out",
		"exit_code":         0,
		"aggregated_output": "wrote out documents and previews",
		"status":            "completed",
	}))
	changes := make([]map[string]any, 0, len(files))
	for _, f := range files {
		changes = append(changes, map[string]any{"path": f, "kind": "update"})
	}
	em.emit(codexItem("item_3", "file_change", map[string]any{
		"changes": changes,
		"status":  "completed",
	}))
	em.emit(codexItem("item_4", "agent_message", map[string]any{
		"text": "Done. The draft documents are in out/, previews in preview/, see manifest.json and SUMMARY.md.",
	}))
	em.emit(map[string]any{"type": "turn.completed", "usage": map[string]any{
		"input_tokens": 4213, "cached_input_tokens": 1024, "output_tokens": 512,
	}})
	return 0
}

// codexItem builds an item.completed event. Both "item_type" (current codex)
// and "type" (older parser table) are set so either parser picks it up.
func codexItem(id, itemType string, fields map[string]any) map[string]any {
	item := map[string]any{"id": id, "item_type": itemType, "type": itemType}
	for k, v := range fields {
		item[k] = v
	}
	return map[string]any{"type": "item.completed", "item": item}
}

// codexFailure emits a few steps followed by turn.failed and exit code 1.
func codexFailure(em *emitter, threadID, message string) int {
	em.emit(map[string]any{"type": "thread.started", "thread_id": threadID})
	em.emit(codexItem("item_1", "reasoning", map[string]any{
		"text": "Starting the draft, but something is about to go wrong.",
	}))
	em.emit(codexItem("item_2", "command_execution", map[string]any{
		"command":           "python3 build_report.py --out out",
		"exit_code":         1,
		"aggregated_output": "traceback: simulated failure",
		"status":            "failed",
	}))
	em.emit(map[string]any{"type": "turn.failed", "error": map[string]any{"message": message}})
	return 1
}

// codexAsk writes QUESTIONS.md once and exits successfully, imitating an
// agent that needs clarification before drafting.
func codexAsk(em *emitter, dir, threadID string) int {
	if err := setFlag(dir, "asked"); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 2
	}
	question := "# Вопросы заказчику\n\n" +
		"1. Какую распределённую нагрузку принимать в расчёте балки — 5 кН/м или 10 кН/м? " +
		"В тексте задания встречаются оба значения.\n"
	if err := os.WriteFile("QUESTIONS.md", []byte(question), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 1
	}

	em.emit(map[string]any{"type": "thread.started", "thread_id": threadID})
	em.emit(codexItem("item_1", "agent_message", map[string]any{
		"text": "Мне нужно уточнение перед началом работы — вопрос записан в QUESTIONS.md.",
	}))
	em.emit(map[string]any{"type": "turn.completed", "usage": map[string]any{
		"input_tokens": 1024, "cached_input_tokens": 0, "output_tokens": 128,
	}})
	return 0
}
