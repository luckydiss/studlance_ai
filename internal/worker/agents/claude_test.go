package agents

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const claudeSimpleSession = "11111111-2222-4333-8444-555555555555"

func TestClaudeSimple(t *testing.T) {
	p := NewClaudeParser("")
	feedFile(t, p, "testdata/claude_simple.jsonl")

	if got := p.SessionID(); got != claudeSimpleSession {
		t.Fatalf("SessionID = %q, want %q", got, claudeSimpleSession)
	}

	steps := p.Steps()
	wantTypes := []string{"reasoning", "message", "command", "message"}
	if got := stepTypes(steps); !slices.Equal(got, wantTypes) {
		t.Fatalf("step types = %v, want %v", got, wantTypes)
	}

	cmd := steps[2]
	if want := "python calc_beam.py --span 6 --load 12"; cmd.Summary != want {
		t.Fatalf("command summary = %q, want %q", cmd.Summary, want)
	}
	if got := cmd.Payload["command"]; got != "python calc_beam.py --span 6 --load 12" {
		t.Fatalf("command payload command = %v", got)
	}
	// The tool_result is attached to the tool_use step via tool_use_id.
	if want := "Mmax = 54.0 kN*m\nselected I-beam 27"; cmd.Payload["result"] != want {
		t.Fatalf("command payload result = %q, want %q", cmd.Payload["result"], want)
	}

	wantUsage := Usage{InputTokens: 15, CachedInputTokens: 4000, OutputTokens: 250, CostUSD: 0.0123}
	if got := p.Usage(); got != wantUsage {
		t.Fatalf("Usage = %+v, want %+v", got, wantUsage)
	}
	if msg, failed := p.Failed(); failed {
		t.Fatalf("Failed = %q, want no failure", msg)
	}
}

func TestClaudeToolResultMutatesSentStep(t *testing.T) {
	p := NewClaudeParser("")
	ts := time.Now().UTC()
	p.Feed([]byte(`{"type":"system","subtype":"init","session_id":"s-1"}`), ts)
	p.Feed([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_X","name":"Bash","input":{"command":"ls"}}]}}`), ts)

	steps := p.Steps()
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(steps))
	}
	before := steps[0]
	if _, ok := before.Payload["result"]; ok {
		t.Fatal("payload already has result before tool_result")
	}

	p.Feed([]byte(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_X","type":"tool_result","content":"file.txt","is_error":false}]}}`), ts)
	if got := before.Payload["result"]; got != "file.txt" {
		t.Fatalf("previously returned step payload result = %v, want %q", got, "file.txt")
	}
}

func TestClaudeFiles(t *testing.T) {
	p := NewClaudeParser("")
	feedFile(t, p, "testdata/claude_files.jsonl")

	steps := p.Steps()
	wantTypes := []string{"file", "file", "web", "command", "error", "message"}
	if got := stepTypes(steps); !slices.Equal(got, wantTypes) {
		t.Fatalf("step types = %v, want %v", got, wantTypes)
	}

	wantPath := `C:\work\job\out\sort.py`
	for i := 0; i < 2; i++ {
		if steps[i].Summary != wantPath {
			t.Fatalf("file step %d summary = %q, want %q", i, steps[i].Summary, wantPath)
		}
		if steps[i].Payload["path"] != wantPath {
			t.Fatalf("file step %d payload path = %v, want %q", i, steps[i].Payload["path"], wantPath)
		}
	}

	if want := "python sort stability timsort"; steps[2].Summary != want {
		t.Fatalf("web summary = %q, want %q", steps[2].Summary, want)
	}

	// tool_result with an array of text blocks is glued together.
	if want := "The file C:\\work\\job\\out\\sort.py has been updated."; steps[1].Payload["result"] != want {
		t.Fatalf("edit payload result = %q, want %q", steps[1].Payload["result"], want)
	}
	if want := "Search results: timsort is stable"; steps[2].Payload["result"] != want {
		t.Fatalf("web payload result = %q, want %q", steps[2].Payload["result"], want)
	}

	// is_error tool_result both attaches and yields an error step.
	errText := `python: can't open file 'out\sort.py': [Errno 2] No such file or directory`
	if steps[3].Payload["result"] != errText {
		t.Fatalf("bash payload result = %q, want %q", steps[3].Payload["result"], errText)
	}
	if steps[4].Summary != errText {
		t.Fatalf("error step summary = %q, want %q", steps[4].Summary, errText)
	}

	wantUsage := Usage{InputTokens: 22, CachedInputTokens: 2100, OutputTokens: 410, CostUSD: 0.0087}
	if got := p.Usage(); got != wantUsage {
		t.Fatalf("Usage = %+v, want %+v", got, wantUsage)
	}
	if msg, failed := p.Failed(); failed {
		t.Fatalf("Failed = %q, want no failure", msg)
	}
}

func TestClaudeError(t *testing.T) {
	p := NewClaudeParser("")
	feedFile(t, p, "testdata/claude_error.jsonl")

	msg, failed := p.Failed()
	if !failed {
		t.Fatal("Failed = false, want true")
	}
	if want := "Reached maximum number of turns (30)"; msg != want {
		t.Fatalf("Failed message = %q, want %q", msg, want)
	}

	wantUsage := Usage{InputTokens: 120, CachedInputTokens: 51000, OutputTokens: 9200, CostUSD: 0.4312}
	if got := p.Usage(); got != wantUsage {
		t.Fatalf("Usage = %+v, want %+v", got, wantUsage)
	}
}

func TestClaudeSessionMatch(t *testing.T) {
	p := NewClaudeParser(claudeSimpleSession)
	feedFile(t, p, "testdata/claude_simple.jsonl")
	if got := p.SessionID(); got != claudeSimpleSession {
		t.Fatalf("SessionID = %q, want %q", got, claudeSimpleSession)
	}
	if msg, failed := p.Failed(); failed {
		t.Fatalf("Failed = %q, want no failure", msg)
	}
}

func TestClaudeSessionMismatch(t *testing.T) {
	p := NewClaudeParser("99999999-0000-4000-8000-000000000000")
	feedFile(t, p, "testdata/claude_simple.jsonl")

	msg, failed := p.Failed()
	if !failed {
		t.Fatal("Failed = false, want true")
	}
	if !strings.Contains(msg, "claude session mismatch") {
		t.Fatalf("Failed message = %q, want a session mismatch", msg)
	}
	if got := p.SessionID(); got != "" {
		t.Fatalf("SessionID = %q, want empty on mismatch", got)
	}
}

func TestClaudeTruncation(t *testing.T) {
	p := NewClaudeParser("")
	ts := time.Now().UTC()

	longText := strings.Repeat("я", 400)
	p.Feed([]byte(fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, longText)), ts)
	steps := p.Steps()
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(steps))
	}
	if got := utf8.RuneCountInString(steps[0].Summary); got != 300 {
		t.Fatalf("summary runes = %d, want 300", got)
	}

	longResult := strings.Repeat("x", 30*1024)
	p.Feed([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_T","name":"Bash","input":{"command":"run"}}]}}`), ts)
	p.Feed([]byte(fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_T","type":"tool_result","content":%q,"is_error":false}]}}`, longResult)), ts)
	steps = p.Steps()
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(steps))
	}
	out, _ := steps[1].Payload["result"].(string)
	if len(out) != 20*1024 {
		t.Fatalf("result bytes = %d, want %d", len(out), 20*1024)
	}
	if out != longResult[len(longResult)-20*1024:] {
		t.Fatal("result is not the last 20 KB of the tool_result text")
	}
}

func TestClaudeGarbage(t *testing.T) {
	p := NewClaudeParser("")
	ts := time.Now().UTC()
	for _, line := range []string{
		"",
		"not json at all",
		`{"type":"stream_event","event":{}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"plain string"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"server_tool_use","id":"x"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"missing","type":"tool_result","content":"orphan"}]}}`,
		`{"type":"system","subtype":"api_retry"}`,
		`{"broken`,
	} {
		p.Feed([]byte(line), ts)
	}
	if got := len(p.Steps()); got != 0 {
		t.Fatalf("steps = %d, want 0", got)
	}
	if _, failed := p.Failed(); failed {
		t.Fatal("Failed = true, want false")
	}
}
