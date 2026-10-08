package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakeagent-bin")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	bin := filepath.Join(dir, "fakeagent")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		panic("go build: " + err.Error() + "\n" + string(out))
	}
	binPath = bin
	os.Exit(m.Run())
}

var codexArgs = []string{
	"codex", "exec", "--json", "--skip-git-repo-check",
	"--dangerously-bypass-approvals-and-sandbox", "-",
}

const claudeSessionID = "11111111-2222-3333-4444-555555555555"

func claudeArgs() []string {
	return []string{
		"claude", "-p", "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions", "--session-id", claudeSessionID,
	}
}

// runAgent runs the built binary in dir with prompt on stdin and returns
// stdout and the exit code.
func runAgent(t *testing.T, dir, prompt string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %v: %v (stderr: %s)", args, err, stderr.String())
		}
		code = exitErr.ExitCode()
	}
	return stdout.String(), code
}

// parseJSONL decodes every stdout line as a JSON object.
func parseJSONL(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("bad JSONL line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

// findEvent returns the first event whose "type" (and optionally "subtype")
// matches, or nil.
func findEvent(events []map[string]any, typ string) map[string]any {
	for _, ev := range events {
		if ev["type"] == typ {
			return ev
		}
	}
	return nil
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func checkManifest(t *testing.T, dir string, wantPreview bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	if m.Title == "" {
		t.Error("manifest: empty title")
	}
	if len(m.Documents) == 0 {
		t.Fatal("manifest: no documents")
	}
	for _, doc := range m.Documents {
		if !strings.HasPrefix(doc.File, "out/") {
			t.Errorf("manifest: file %q does not start with out/", doc.File)
		}
		if _, err := os.Stat(filepath.Join(dir, doc.File)); err != nil {
			t.Errorf("manifest: file %q missing: %v", doc.File, err)
		}
		if wantPreview {
			if !strings.HasPrefix(doc.Preview, "preview/") {
				t.Errorf("manifest: preview %q does not start with preview/", doc.Preview)
			}
			if _, err := os.Stat(filepath.Join(dir, doc.Preview)); err != nil {
				t.Errorf("manifest: preview %q missing: %v", doc.Preview, err)
			}
		}
	}
}

func TestCodexDraft(t *testing.T) {
	dir := t.TempDir()
	stdout, code := runAgent(t, dir, "Выполни практическую работу по расчёту балки.", nil, codexArgs...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	for _, f := range []string{
		"out/Пояснительная записка.docx", "out/Расчёт.xlsx",
		"preview/Пояснительная записка.pdf", "preview/Расчёт.pdf",
		"manifest.json", "SUMMARY.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	checkManifest(t, dir, true)

	firstLine, _, _ := strings.Cut(readFile(t, filepath.Join(dir, "SUMMARY.md")), "\n")
	if firstLine != "Практическая работа: расчёт балки" {
		t.Errorf("SUMMARY.md first line = %q", firstLine)
	}

	events := parseJSONL(t, stdout)
	if findEvent(events, "thread.started") == nil {
		t.Error("no thread.started event")
	}
	turn := findEvent(events, "turn.completed")
	if turn == nil {
		t.Fatal("no turn.completed event")
	}
	usage, ok := turn["usage"].(map[string]any)
	if !ok || usage["input_tokens"] == nil || usage["output_tokens"] == nil {
		t.Errorf("turn.completed without usage: %v", turn)
	}
}

func TestCodexResumeKeepsThreadID(t *testing.T) {
	dir := t.TempDir()
	args := []string{
		"codex", "exec", "resume", "--json", "--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox", "thread-abc123", "-",
	}
	stdout, code := runAgent(t, dir, "Продолжи работу.", nil, args...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	ev := findEvent(parseJSONL(t, stdout), "thread.started")
	if ev == nil {
		t.Fatal("no thread.started event")
	}
	if ev["thread_id"] != "thread-abc123" {
		t.Errorf("thread_id = %v, want thread-abc123", ev["thread_id"])
	}
}

func TestCodexLongTraceKeepsAllSyntheticSteps(t *testing.T) {
	dir := t.TempDir()
	stdout, code := runAgent(t, dir, "Синтетическая работа #longtrace", nil, codexArgs...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var longSteps int
	for _, ev := range parseJSONL(t, stdout) {
		if ev["type"] != "item.completed" {
			continue
		}
		item, ok := ev["item"].(map[string]any)
		if !ok || item["item_type"] != "reasoning" {
			continue
		}
		fields, _ := item["text"].(string)
		if strings.HasPrefix(fields, "Шаг ") {
			longSteps++
		}
	}
	if longSteps != longTraceSteps {
		t.Fatalf("synthetic long-trace steps = %d, want %d", longSteps, longTraceSteps)
	}
	if findEvent(parseJSONL(t, stdout), "turn.completed") == nil {
		t.Fatal("long trace did not finish normally")
	}
	checkManifest(t, dir, true)
}

// prepareDraft runs a normal codex draft in dir.
func prepareDraft(t *testing.T, dir string) {
	t.Helper()
	if _, code := runAgent(t, dir, "Выполни практическую работу.", nil, codexArgs...); code != 0 {
		t.Fatalf("draft: exit code = %d", code)
	}
}

func TestClaudeVerify(t *testing.T) {
	dir := t.TempDir()
	prepareDraft(t, dir)
	before := readFile(t, filepath.Join(dir, "preview", "Пояснительная записка.pdf"))

	stdout, code := runAgent(t, dir, "Проверь работу.", nil, claudeArgs()...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	events := parseJSONL(t, stdout)
	init := findEvent(events, "system")
	if init == nil {
		t.Fatal("no system init event")
	}
	if init["session_id"] != claudeSessionID {
		t.Errorf("session_id = %v, want %s", init["session_id"], claudeSessionID)
	}
	result := findEvent(events, "result")
	if result == nil || result["is_error"] != false {
		t.Errorf("bad result event: %v", result)
	}

	after := readFile(t, filepath.Join(dir, "preview", "Пояснительная записка.pdf"))
	if before == after {
		t.Error("preview pdf unchanged after verify")
	}

	var v verificationFile
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "verification.json"))), &v); err != nil {
		t.Fatalf("verification.json: %v", err)
	}
	if v.Found == 0 || v.Fixed == 0 {
		t.Errorf("verification.json: found=%d fixed=%d", v.Found, v.Fixed)
	}
	checkManifest(t, dir, true)
}

func TestAsk(t *testing.T) {
	dir := t.TempDir()
	_, code := runAgent(t, dir, "Сделай работу. #ask", nil, codexArgs...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	qpath := filepath.Join(dir, "QUESTIONS.md")
	if strings.TrimSpace(readFile(t, qpath)) == "" {
		t.Fatal("QUESTIONS.md is empty")
	}

	// A resume with the same marker must do the normal work and leave the
	// existing QUESTIONS.md untouched.
	if err := os.WriteFile(qpath, []byte("sentinel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resumeArgs := []string{
		"codex", "exec", "resume", "--json", "--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox", "thread-ask1", "-",
	}
	_, code = runAgent(t, dir, "Сделай работу. #ask", nil, resumeArgs...)
	if code != 0 {
		t.Fatalf("resume exit code = %d, want 0", code)
	}
	if got := readFile(t, qpath); got != "sentinel\n" {
		t.Errorf("QUESTIONS.md overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "Пояснительная записка.docx")); err != nil {
		t.Error("resume did not produce out/ files")
	}
}

func TestFailOnce(t *testing.T) {
	dir := t.TempDir()
	_, code := runAgent(t, dir, "#fail-once", nil, codexArgs...)
	if code != 1 {
		t.Fatalf("first attempt exit code = %d, want 1", code)
	}
	_, code = runAgent(t, dir, "#fail-once", nil, codexArgs...)
	if code != 0 {
		t.Fatalf("second attempt exit code = %d, want 0", code)
	}
}

func TestFailVerify(t *testing.T) {
	dir := t.TempDir()
	prepareDraft(t, dir)
	for i := range 2 {
		stdout, code := runAgent(t, dir, "#fail-verify", nil, claudeArgs()...)
		if code != 1 {
			t.Fatalf("attempt %d: exit code = %d, want 1", i+1, code)
		}
		result := findEvent(parseJSONL(t, stdout), "result")
		if result == nil || result["is_error"] != true || result["subtype"] != "error_during_execution" {
			t.Errorf("attempt %d: bad result event: %v", i+1, result)
		}
	}
}

func TestHang(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(binPath, codexArgs...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("#hang")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	pidFile := filepath.Join(dir, ".fakeagent", "hang.pid")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hang.pid was not created within 2s")
		}
		time.Sleep(50 * time.Millisecond)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("process exited on its own: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	cmd.Process.Kill()
	<-done
}

func TestNoPDF(t *testing.T) {
	dir := t.TempDir()
	_, code := runAgent(t, dir, "#no-pdf", nil, codexArgs...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "preview")); !os.IsNotExist(err) {
		t.Errorf("preview/ exists (stat err = %v)", err)
	}

	var m manifest
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "manifest.json"))), &m); err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	if len(m.Documents) == 0 {
		t.Fatal("manifest: no documents")
	}
	for _, doc := range m.Documents {
		if doc.Preview != "" {
			if _, err := os.Stat(filepath.Join(dir, doc.Preview)); err != nil {
				t.Errorf("manifest points to missing preview %q", doc.Preview)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, doc.File)); err != nil {
			t.Errorf("manifest points to missing file %q", doc.File)
		}
	}
}

func TestRevise(t *testing.T) {
	dir := t.TempDir()
	prepareDraft(t, dir)
	if _, code := runAgent(t, dir, "Проверь работу.", nil, claudeArgs()...); code != 0 {
		t.Fatalf("verify: exit code = %d", code)
	}
	beforeVerification := readFile(t, filepath.Join(dir, "verification.json"))

	revision := "# Доработка — версия 2\n\n## Замечания на листах\n" +
		"1. «Неверное значение» — Пояснительная записка (out/Пояснительная записка.docx), стр. 1, область: x=0.1, y=0.1, w=0.2, h=0.05\n"
	if err := os.WriteFile(filepath.Join(dir, "REVISION-2.md"), []byte(revision), 0o644); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"claude", "-p", "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions", "--resume", claudeSessionID,
	}
	_, code := runAgent(t, dir, "Выполни доработку по REVISION-2.md.", nil, args...)
	if code != 0 {
		t.Fatalf("revise exit code = %d, want 0", code)
	}

	if !strings.Contains(readFile(t, filepath.Join(dir, "VERIFICATION.md")), "Доработка 2") {
		t.Error("VERIFICATION.md has no «Доработка 2» section")
	}
	afterVerification := readFile(t, filepath.Join(dir, "verification.json"))
	if afterVerification == beforeVerification {
		t.Error("verification.json unchanged after revise")
	}
	var v verificationFile
	if err := json.Unmarshal([]byte(afterVerification), &v); err != nil {
		t.Fatalf("verification.json: %v", err)
	}
	if v.Found != 4 || v.Fixed != 4 {
		t.Errorf("verification.json: found=%d fixed=%d, want 4/4", v.Found, v.Fixed)
	}
}

func TestScriptEnvMarker(t *testing.T) {
	dir := t.TempDir()
	env := []string{`FAKEAGENT_SCRIPT={"marker":"#ask","sleep_ms":0}`}
	_, code := runAgent(t, dir, "Сделай работу.", env, codexArgs...)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.TrimSpace(readFile(t, filepath.Join(dir, "QUESTIONS.md"))) == "" {
		t.Fatal("QUESTIONS.md is empty (FAKEAGENT_SCRIPT marker was not applied)")
	}
}
