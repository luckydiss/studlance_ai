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
	wantTypes := []string{"reasoning", "command", "file", "message"}
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
	wantTypes := []string{"web", "tool", "message", "message"}
	if got := stepTypes(steps); !slices.Equal(got, wantTypes) {
		t.Fatalf("step types = %v, want %v", got, wantTypes)
	}
	if want := "bubble sort example python"; steps[0].Summary != want {
		t.Fatalf("web summary = %q, want %q", steps[0].Summary, want)
	}
	if want := "docs/lookup"; steps[1].Summary != want {
		t.Fatalf("tool summary = %q, want %q", steps[1].Summary, want)
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
	wantTypes := []string{"reasoning", "error"}
	if got := stepTypes(steps); !slices.Equal(got, wantTypes) {
		t.Fatalf("step types = %v, want %v", got, wantTypes)
	}
	if steps[1].Summary != msg {
		t.Fatalf("error step summary = %q, want %q", steps[1].Summary, msg)
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
		`{"broken`,
	} {
		p.Feed([]byte(line), ts)
	}
	if got := len(p.Steps()); got != 0 {
		t.Fatalf("steps = %d, want 0", got)
	}
	if _, failed := p.Failed(); failed {
		t.Fatal("Failed = true on empty turn.failed, want false")
	}
}
