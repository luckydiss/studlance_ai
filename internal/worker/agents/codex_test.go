package agents

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// feedFile feeds every line of a testdata log into p.
func feedFile(t *testing.T, p Parser, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	ts := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	for sc.Scan() {
		p.Feed(sc.Bytes(), ts)
		ts = ts.Add(time.Second)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
}

func stepTypes(steps []Step) []string {
	types := make([]string, len(steps))
	for i, s := range steps {
		types[i] = s.Type
	}
	return types
}

func TestCodexSimple(t *testing.T) {
	p := NewCodexParser()
	feedFile(t, p, "testdata/codex_simple.jsonl")

	if got, want := p.ThreadID(), "0199aabb-ccdd-7000-8000-000000000001"; got != want {
		t.Fatalf("ThreadID = %q, want %q", got, want)
	}

	steps := p.Steps()
	wantTypes := []string{"reasoning", "command", "file", "message", "result"}
	if got := stepTypes(steps); !slices.Equal(got, wantTypes) {
		t.Fatalf("step types = %v, want %v", got, wantTypes)
	}

	cmd := steps[1]
	if want := "python calc_beam.py --span 6 --load 12"; cmd.Summary != want {
		t.Fatalf("command summary = %q, want %q", cmd.Summary, want)
	}
	if got := cmd.Payload["command"]; got != "python calc_beam.py --span 6 --load 12" {
		t.Fatalf("command payload command = %v", got)
	}
	if got := cmd.Payload["exit_code"]; got != 0 {
		t.Fatalf("command payload exit_code = %v, want 0", got)
	}
	out, _ := cmd.Payload["output"].(string)
	if !strings.Contains(out, "reading input data") || !strings.Contains(out, "selected I-beam 27") {
		t.Fatalf("command output = %q, want the full aggregated_output", out)
	}

	file := steps[2]
	if want := "изменены файлы: out/beam_report.md, calc_beam.py"; file.Summary != want {
		t.Fatalf("file summary = %q, want %q", file.Summary, want)
	}
	changes, ok := file.Payload["changes"].([]map[string]interface{})
	if !ok || len(changes) != 2 {
		t.Fatalf("file payload changes = %#v, want 2 entries", file.Payload["changes"])
	}

	if want := "Готово: балка рассчитана, принят двутавр 27. Отчёт — в out/beam_report.md."; steps[3].Summary != want {
		t.Fatalf("message summary = %q, want %q", steps[3].Summary, want)
	}

	// turn.completed yields a terminal result step with the token counters.
	res := steps[4]
	if want := "готово: 5.1k токенов"; res.Summary != want {
		t.Fatalf("result summary = %q, want %q", res.Summary, want)
	}
	if got := res.Payload["input_tokens"]; got != 4200 {
		t.Fatalf("result payload input_tokens = %v, want 4200", got)
	}
	if got := res.Payload["cached_input_tokens"]; got != 1500 {
		t.Fatalf("result payload cached_input_tokens = %v, want 1500", got)
	}
	if got := res.Payload["output_tokens"]; got != 860 {
		t.Fatalf("result payload output_tokens = %v, want 860", got)
	}

	wantUsage := Usage{InputTokens: 4200, CachedInputTokens: 1500, OutputTokens: 860}
	if got := p.Usage(); got != wantUsage {
		t.Fatalf("Usage = %+v, want %+v", got, wantUsage)
	}
	if msg, failed := p.Failed(); failed {
		t.Fatalf("Failed = %q, want no failure", msg)
	}
}

func TestCodexSearch(t *testing.T) {
	p := NewCodexParser()
	feedFile(t, p, "testdata/codex_search.jsonl")

	steps := p.Steps()
	wantTypes := []string{"web", "tool", "message", "result", "message", "result"}
	if got := stepTypes(steps); !slices.Equal(got, wantTypes) {
		t.Fatalf("step types = %v, want %v", got, wantTypes)
	}
	if want := "bubble sort example python"; steps[0].Summary != want {
		t.Fatalf("web summary = %q, want %q", steps[0].Summary, want)
	}
	if want := "docs/lookup"; steps[1].Summary != want {
		t.Fatalf("tool summary = %q, want %q", steps[1].Summary, want)
	}

	// Every turn.completed yields its own result step.
	if got := steps[3].Payload["input_tokens"]; got != 1000 {
		t.Fatalf("first result payload input_tokens = %v, want 1000", got)
	}
	if got := steps[5].Payload["input_tokens"]; got != 500 {
		t.Fatalf("second result payload input_tokens = %v, want 500", got)
	}

	// Usage sums over both turn.completed events.
	wantUsage := Usage{InputTokens: 1500, CachedInputTokens: 150, OutputTokens: 320}
	if got := p.Usage(); got != wantUsage {
		t.Fatalf("Usage = %+v, want %+v", got, wantUsage)
	}
	if msg, failed := p.Failed(); failed {
		t.Fatalf("Failed = %q, want no failure", msg)
	}
}

func TestCodexFailed(t *testing.T) {
	p := NewCodexParser()
	feedFile(t, p, "testdata/codex_failed.jsonl")

	msg, failed := p.Failed()
	if !failed {
		t.Fatal("Failed = false, want true")
	}
	if want := "stream disconnected before completion: network error"; msg != want {
		t.Fatalf("Failed message = %q, want %q", msg, want)
	}

	steps := p.Steps()
	wantTypes := []string{"reasoning", "error", "error"}
	if got := stepTypes(steps); !slices.Equal(got, wantTypes) {
		t.Fatalf("step types = %v, want %v", got, wantTypes)
	}
	// The {"type":"error"} event is a plain error step; only turn.failed fails.
	if want := "stream error: reconnecting"; steps[1].Summary != want {
		t.Fatalf("error step summary = %q, want %q", steps[1].Summary, want)
	}
	if steps[2].Summary != msg {
		t.Fatalf("turn.failed step summary = %q, want %q", steps[2].Summary, msg)
	}
}

// TestCodexErrorEventNotFatal: a {"type":"error"} event alone never fails
// the run.
func TestCodexErrorEventNotFatal(t *testing.T) {
	p := NewCodexParser()
	ts := time.Now().UTC()
	p.Feed([]byte(`{"type":"error","message":"stream error: reconnecting"}`), ts)

	steps := p.Steps()
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(steps))
	}
	if steps[0].Type != "error" {
		t.Fatalf("step type = %q, want error", steps[0].Type)
	}
	if want := "stream error: reconnecting"; steps[0].Summary != want {
		t.Fatalf("step summary = %q, want %q", steps[0].Summary, want)
	}
	if msg, failed := p.Failed(); failed {
		t.Fatalf("Failed = %q, want false for an error event", msg)
	}
}

func TestCodexTruncation(t *testing.T) {
	p := NewCodexParser()
	ts := time.Now().UTC()

	longText := strings.Repeat("я", 400) // 400 runes, 800 bytes
	p.Feed([]byte(fmt.Sprintf(`{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":%q}}`, longText)), ts)
	steps := p.Steps()
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(steps))
	}
	if got := utf8.RuneCountInString(steps[0].Summary); got != 300 {
		t.Fatalf("summary runes = %d, want 300", got)
	}

	longOutput := strings.Repeat("x", 30*1024)
	p.Feed([]byte(fmt.Sprintf(`{"type":"item.completed","item":{"id":"i2","type":"command_execution","command":"run","aggregated_output":%q,"exit_code":0}}`, longOutput)), ts)
	steps = p.Steps()
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(steps))
	}
	out, _ := steps[1].Payload["output"].(string)
	if len(out) != 20*1024 {
		t.Fatalf("output bytes = %d, want %d", len(out), 20*1024)
	}
	if out != longOutput[len(longOutput)-20*1024:] {
		t.Fatal("output is not the last 20 KB of aggregated_output")
	}
}

func TestCodexGarbage(t *testing.T) {
	p := NewCodexParser()
	ts := time.Now().UTC()
	for _, line := range []string{
		"",
		"not json at all",
		`{"type":"unknown.event","foo":1}`,
		`{"type":"item.completed"}`,
		`{"type":"item.completed","item":{"type":"todo_list","items":[]}}`,
		`{"type":"turn.completed"}`,
		`{"type":"turn.failed"}`,
		`{"type":"error"}`,
		`{"broken`,
	} {
		p.Feed([]byte(line), ts)
	}
	if got := len(p.Steps()); got != 0 {
		t.Fatalf("steps = %d, want 0", got)
	}
	if _, failed := p.Failed(); failed {
		t.Fatal("Failed = true on empty turn.failed/error, want false")
	}
}

// TestCodexRealOutOfCredits parses a real codex-cli 0.160.0 log (recorded on
// 2026-10-05; the workspace was out of credits): the type:"error" event is a
// trace step only, turn.failed fails the stage.
func TestCodexRealOutOfCredits(t *testing.T) {
	p := NewCodexParser()
	feedFile(t, p, "testdata/codex_real_out_of_credits.jsonl")
	if p.ThreadID() != "01a10d25-2c96-7992-bff7-7d0ce145fcce" {
		t.Fatalf("thread = %q", p.ThreadID())
	}
	var errSteps int
	for _, s := range p.Steps() {
		if s.Type == "error" {
			errSteps++
		}
	}
	if errSteps == 0 {
		t.Fatal("no error step for the type:error event")
	}
	msg, failed := p.Failed()
	if !failed || !strings.Contains(msg, "out of credits") {
		t.Fatalf("Failed = %v %q", failed, msg)
	}
}

// TestCodexRealBeamPartial parses a real codex-cli 0.160.0 log (recorded
// 2026-10-05 in C:\work\job on a fictional beam-calc task; the workspace ran
// out of credits mid-run, hence the trailing error + turn.failed). Sanitized:
// username replaced, no personal data. It is kept as an explicitly partial
// run; the successful run and its resume are recorded in
// codex_real_beam.jsonl / codex_real_beam_resume.jsonl (TestCodexRealBeam).
func TestCodexRealBeamPartial(t *testing.T) {
	p := NewCodexParser()
	feedFile(t, p, "testdata/codex_real_beam_partial.jsonl")
	if p.ThreadID() == "" {
		t.Fatal("no thread id")
	}
	types := map[string]int{}
	for _, s := range p.Steps() {
		types[s.Type]++
	}
	for _, want := range []string{"command", "file", "message", "web", "error"} {
		if types[want] == 0 {
			t.Fatalf("no %q step; types = %v", want, types)
		}
	}
	msg, failed := p.Failed()
	if !failed || !strings.Contains(msg, "out of credits") {
		t.Fatalf("Failed = %v %q", failed, msg)
	}
	// Every command step keeps the 300-rune summary limit on real data.
	for _, s := range p.Steps() {
		if len([]rune(s.Summary)) > 300 {
			t.Fatalf("summary over 300 runes: %.40s", s.Summary)
		}
	}
}

// TestCodexRealBeam parses the recorded successful codex-cli 0.160.0 run and
// its resume (2026-10-06, fictional beam-calc task in C:\work\job; the user
// name and the working folder are replaced, there are no model identifiers).
func TestCodexRealBeam(t *testing.T) {
	p := NewCodexParser()
	feedFile(t, p, "testdata/codex_real_beam.jsonl")
	if p.ThreadID() == "" {
		t.Fatal("no thread id")
	}
	if _, failed := p.Failed(); failed {
		t.Fatal("a successful run reported a failure")
	}
	types := map[string]int{}
	for _, s := range p.Steps() {
		types[s.Type]++
	}
	for _, want := range []string{"command", "file", "message", "result"} {
		if types[want] == 0 {
			t.Fatalf("no %q step; types = %v", want, types)
		}
	}
	if u := p.Usage(); u.InputTokens <= 0 || u.OutputTokens <= 0 {
		t.Fatalf("usage = %+v", u)
	}

	// The resume continues the same thread and is successful too.
	q := NewCodexParser()
	feedFile(t, q, "testdata/codex_real_beam_resume.jsonl")
	if q.ThreadID() != p.ThreadID() {
		t.Fatalf("resume thread id = %q, want %q", q.ThreadID(), p.ThreadID())
	}
	if _, failed := q.Failed(); failed {
		t.Fatal("a successful resume reported a failure")
	}
	if u := q.Usage(); u.InputTokens <= 0 || u.OutputTokens <= 0 {
		t.Fatalf("resume usage = %+v", u)
	}
}
