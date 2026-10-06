package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// runClaude imitates `claude -p --output-format stream-json --verbose`:
// the verify stage, or the revise stage when a REVISION-<n>.md exists and
// the prompt references it.
func runClaude(cfg *config) int {
	if cfg.markers["#hang"] {
		return hang()
	}

	sessionID := cfg.sessionID
	if sessionID == "" {
		sessionID = "00000000-0000-4000-8000-" + randHex(6)
	}
	em := newEmitter(cfg.sleepMs)

	if cfg.markers["#fail-verify"] {
		return claudeFailure(em, sessionID)
	}
	if cfg.markers["#fail-verify-once"] || cfg.markers["#fail-verify-twice"] {
		dir, err := stateDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakeagent:", err)
			return 2
		}
		if !flagExists(dir, "failed-verify-1") {
			if err := setFlag(dir, "failed-verify-1"); err != nil {
				fmt.Fprintln(os.Stderr, "fakeagent:", err)
				return 2
			}
			return claudeFailure(em, sessionID)
		}
		if cfg.markers["#fail-verify-twice"] && !flagExists(dir, "failed-verify-2") {
			if err := setFlag(dir, "failed-verify-2"); err != nil {
				fmt.Fprintln(os.Stderr, "fakeagent:", err)
				return 2
			}
			return claudeFailure(em, sessionID)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 2
	}
	if n := latestRevision(cwd); n > 0 && referencesRevision(cfg.prompt) {
		return claudeRevise(em, cwd, sessionID, n)
	}
	return claudeVerify(em, cwd, sessionID)
}

// claudeVerify rewrites the preview PDFs with changed page-2 text and adds
// the verification report files.
func claudeVerify(em *emitter, cwd, sessionID string) int {
	if err := verifyPreviews(cwd); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 1
	}
	if err := writeVerification(cwd, 3, 3); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 1
	}

	em.emit(claudeSystem(sessionID))
	em.emit(claudeMessage(sessionID, "msg_1", []map[string]any{
		{"type": "thinking", "thinking": "Checking the draft documents against the task: numbers, formatting, previews."},
		{"type": "text", "text": "I will re-check the documents and regenerate the previews with fixes."},
	}))
	em.emit(claudeMessage(sessionID, "msg_2", []map[string]any{
		{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{"command": "python3 verify_report.py"}},
	}))
	em.emit(claudeToolResult(sessionID, "toolu_1", "ok"))
	em.emit(claudeMessage(sessionID, "msg_3", []map[string]any{
		{"type": "tool_use", "id": "toolu_2", "name": "Write", "input": map[string]any{"file_path": "VERIFICATION.md"}},
	}))
	em.emit(claudeResult(sessionID, "success", false))
	return 0
}

// claudeRevise applies revision <n>: rewrites the mentioned preview pages,
// appends a VERIFICATION.md section and updates verification.json.
func claudeRevise(em *emitter, cwd, sessionID string, n int) int {
	data, err := os.ReadFile(filepath.Join(cwd, fmt.Sprintf("REVISION-%d.md", n)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 1
	}
	pages := revisionPages(string(data))
	if err := revisePreviews(cwd, n, pages); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 1
	}
	if err := appendRevisionNote(cwd, n, pages); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 1
	}
	if err := bumpVerification(cwd, len(pages)); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 1
	}

	em.emit(claudeSystem(sessionID))
	em.emit(claudeMessage(sessionID, "msg_1", []map[string]any{
		{"type": "thinking", "thinking": fmt.Sprintf("Revision %d asks to fix pages %v.", n, pages)},
		{"type": "text", "text": fmt.Sprintf("Applying revision %d to the documents and previews.", n)},
	}))
	em.emit(claudeMessage(sessionID, "msg_2", []map[string]any{
		{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{"command": "python3 apply_revision.py"}},
	}))
	em.emit(claudeToolResult(sessionID, "toolu_1", "ok"))
	em.emit(claudeMessage(sessionID, "msg_3", []map[string]any{
		{"type": "tool_use", "id": "toolu_2", "name": "Write", "input": map[string]any{"file_path": "VERIFICATION.md"}},
	}))
	em.emit(claudeResult(sessionID, "success", false))
	return 0
}

// claudeFailure emits a few steps and a failed result, exit code 1.
func claudeFailure(em *emitter, sessionID string) int {
	em.emit(claudeSystem(sessionID))
	em.emit(claudeMessage(sessionID, "msg_1", []map[string]any{
		{"type": "thinking", "thinking": "Starting verification, but it will not finish."},
		{"type": "text", "text": "Running the verification checks."},
	}))
	em.emit(claudeMessage(sessionID, "msg_2", []map[string]any{
		{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{"command": "python3 verify_report.py"}},
	}))
	em.emit(claudeToolResult(sessionID, "toolu_1", "check failed"))
	em.emit(claudeResult(sessionID, "error_during_execution", true))
	return 1
}

func claudeSystem(sessionID string) map[string]any {
	return map[string]any{
		"type": "system", "subtype": "init", "session_id": sessionID,
		"tools": []string{"Bash", "Write"}, "model": "fake-claude",
	}
}

func claudeMessage(sessionID, id string, content []map[string]any) map[string]any {
	return map[string]any{
		"type": "assistant", "session_id": sessionID,
		"message": map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": "fake-claude",
			"content": content,
		},
	}
}

func claudeToolResult(sessionID, toolUseID, content string) map[string]any {
	return map[string]any{
		"type": "user", "session_id": sessionID,
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{"type": "tool_result", "tool_use_id": toolUseID, "content": content},
			},
		},
	}
}

func claudeResult(sessionID, subtype string, isError bool) map[string]any {
	return map[string]any{
		"type": "result", "subtype": subtype, "is_error": isError,
		"duration_ms": 1234, "num_turns": 2, "session_id": sessionID,
		"total_cost_usd": 0.01,
		"usage": map[string]any{
			"input_tokens": 1500, "output_tokens": 400, "cache_read_input_tokens": 800,
		},
	}
}

var revisionFileRe = regexp.MustCompile(`^REVISION-(\d+)\.md$`)

// latestRevision returns the highest n for which REVISION-<n>.md exists in
// dir, or 0 when there is none.
func latestRevision(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var nums []int
	for _, e := range entries {
		if m := revisionFileRe.FindStringSubmatch(e.Name()); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				nums = append(nums, n)
			}
		}
	}
	if len(nums) == 0 {
		return 0
	}
	sort.Ints(nums)
	return nums[len(nums)-1]
}

// referencesRevision reports whether the prompt points at a revision file.
func referencesRevision(prompt string) bool {
	return strings.Contains(prompt, "REVISION-") || strings.Contains(prompt, "доработ")
}

var pageRefRe = regexp.MustCompile(`стр\.\s*(\d+)`)

// revisionPages extracts the mentioned page numbers ("стр. N") from a
// REVISION-<n>.md; defaults to page 1 when none are named.
func revisionPages(text string) []int {
	seen := map[int]bool{}
	var pages []int
	for _, m := range pageRefRe.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 1 && !seen[n] {
			seen[n] = true
			pages = append(pages, n)
		}
	}
	if len(pages) == 0 {
		pages = []int{1}
	}
	sort.Ints(pages)
	return pages
}
