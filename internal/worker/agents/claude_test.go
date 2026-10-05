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
	wantTypes := []string{"reasoning", "message", "command", "tool_result", "message", "result"}
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

	// The tool_result is its own step, linked via payload.tool_use_id.
	tr := steps[3]
	if want := "Mmax = 54.0 kN*m\nselected I-beam 27"; tr.Payload["result"] != want {
		t.Fatalf("tool_result payload result = %q, want %q", tr.Payload["result"], want)
	}
	if want := "toolu_01AAA"; tr.Payload["tool_use_id"] != want {
		t.Fatalf("tool_result payload tool_use_id = %v, want %q", tr.Payload["tool_use_id"], want)
	}

	// The result event appends a terminal result step; the input total
	// includes cache creation and cache read tokens.
	res := steps[5]
	if want := "готово: 5.2k токенов, $0.01"; res.Summary != want {
		t.Fatalf("result summary = %q, want %q", res.Summary, want)
	}
	if got := res.Payload["input_tokens"]; got != 4915 {
		t.Fatalf("result payload input_tokens = %v, want 4915", got)
	}
	if got := res.Payload["output_tokens"]; got != 250 {
		t.Fatalf("result payload output_tokens = %v, want 250", got)
	}
	if got := res.Payload["cached_input_tokens"]; got != 4000 {
		t.Fatalf("result payload cached_input_tokens = %v, want 4000", got)
	}
	if got := res.Payload["cost_usd"]; got != 0.0123 {
		t.Fatalf("result payload cost_usd = %v, want 0.0123", got)
	}
	if got := res.Payload["subtype"]; got != "success" {
		t.Fatalf("result payload subtype = %v, want success", got)
	}
	if got := res.Payload["is_error"]; got != false {
		t.Fatalf("result payload is_error = %v, want false", got)
	}

	wantUsage := Usage{InputTokens: 4915, CachedInputTokens: 4000, OutputTokens: 250, CostUSD: 0.0123}
	if got := p.Usage(); got != wantUsage {
		t.Fatalf("Usage = %+v, want %+v", got, wantUsage)
	}
	if msg, failed := p.Failed(); failed {
		t.Fatalf("Failed = %q, want no failure", msg)
	}
}

// TestClaudeToolResultSeparateStep: every user tool_result is its own step
// (never a mutation of the tool_use step); is_error makes it an error step.
func TestClaudeToolResultSeparateStep(t *testing.T) {
	p := NewClaudeParser("")
	ts := time.Now().UTC()
	p.Feed([]byte(`{"type":"system","subtype":"init","session_id":"s-1"}`), ts)
	p.Feed([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_X","name":"Bash","input":{"command":"ls"}}]}}`), ts)
	p.Feed([]byte(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_X","type":"tool_result","content":"file.txt","is_error":false}]}}`), ts)

	steps := p.Steps()
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(steps))
	}
	if steps[0].Type != "command" {
		t.Fatalf("step 0 type = %q, want command", steps[0].Type)
	}
	if _, ok := steps[0].Payload["result"]; ok {
		t.Fatal("tool_use step payload must not carry the tool result")
	}
	if steps[1].Type != "tool_result" {
		t.Fatalf("step 1 type = %q, want tool_result", steps[1].Type)
	}
	if got := steps[1].Payload["tool_use_id"]; got != "toolu_X" {
		t.Fatalf("tool_result payload tool_use_id = %v, want %q", got, "toolu_X")
	}
	if got := steps[1].Payload["result"]; got != "file.txt" {
		t.Fatalf("tool_result payload result = %v, want %q", got, "file.txt")
	}

	// A failed tool_result becomes an error step with the same payload.
	p.Feed([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_Y","name":"Bash","input":{"command":"rm x"}}]}}`), ts)
	p.Feed([]byte(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_Y","type":"tool_result","content":"permission denied","is_error":true}]}}`), ts)
	steps = p.Steps()
	if len(steps) != 4 {
		t.Fatalf("steps = %d, want 4", len(steps))
	}
	if steps[3].Type != "error" {
		t.Fatalf("step 3 type = %q, want error", steps[3].Type)
	}
	if got := steps[3].Payload["tool_use_id"]; got != "toolu_Y" {
		t.Fatalf("error payload tool_use_id = %v, want %q", got, "toolu_Y")
	}
	if got := steps[3].Payload["result"]; got != "permission denied" {
		t.Fatalf("error payload result = %v, want %q", got, "permission denied")
	}

	// An orphan tool_result (no matching tool_use) still yields a step.
	p.Feed([]byte(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"missing","type":"tool_result","content":"orphan"}]}}`), ts)
	steps = p.Steps()
	if len(steps) != 5 {
		t.Fatalf("steps = %d, want 5", len(steps))
	}
	if steps[4].Type != "tool_result" {
		t.Fatalf("step 4 type = %q, want tool_result", steps[4].Type)
	}
	if got := steps[4].Payload["tool_use_id"]; got != "missing" {
		t.Fatalf("orphan payload tool_use_id = %v, want %q", got, "missing")
	}
}

func TestClaudeFiles(t *testing.T) {
	p := NewClaudeParser("")
	feedFile(t, p, "testdata/claude_files.jsonl")

	steps := p.Steps()
	wantTypes := []string{"file", "file", "tool_result", "web", "tool_result", "command", "error", "message", "result"}
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

	// tool_result with an array of text blocks is glued together.
	if want := "The file C:\\work\\job\\out\\sort.py has been updated."; steps[2].Payload["result"] != want {
		t.Fatalf("edit tool_result = %q, want %q", steps[2].Payload["result"], want)
	}
	if want := "toolu_03CCC"; steps[2].Payload["tool_use_id"] != want {
		t.Fatalf("edit tool_result tool_use_id = %v, want %q", steps[2].Payload["tool_use_id"], want)
	}

	if want := "python sort stability timsort"; steps[3].Summary != want {
		t.Fatalf("web summary = %q, want %q", steps[3].Summary, want)
	}
	if want := "Search results: timsort is stable"; steps[4].Payload["result"] != want {
		t.Fatalf("web tool_result = %q, want %q", steps[4].Payload["result"], want)
	}

	// An is_error tool_result becomes an error step.
	errText := `python: can't open file 'out\sort.py': [Errno 2] No such file or directory`
	if steps[6].Payload["result"] != errText {
		t.Fatalf("error payload result = %q, want %q", steps[6].Payload["result"], errText)
	}
	if want := "toolu_05EEE"; steps[6].Payload["tool_use_id"] != want {
		t.Fatalf("error payload tool_use_id = %v, want %q", steps[6].Payload["tool_use_id"], want)
	}
	if steps[6].Summary != errText {
		t.Fatalf("error step summary = %q, want %q", steps[6].Summary, errText)
	}

	wantUsage := Usage{InputTokens: 2622, CachedInputTokens: 2100, OutputTokens: 410, CostUSD: 0.0087}
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

	wantUsage := Usage{InputTokens: 54120, CachedInputTokens: 51000, OutputTokens: 9200, CostUSD: 0.4312}
	if got := p.Usage(); got != wantUsage {
		t.Fatalf("Usage = %+v, want %+v", got, wantUsage)
	}

	// The terminal result step reports the failure.
	steps := p.Steps()
	if got := stepTypes(steps); !slices.Equal(got, []string{"message", "result"}) {
		t.Fatalf("step types = %v, want [message result]", got)
	}
	res := steps[1]
	if want := "ошибка: Reached maximum number of turns (30)"; res.Summary != want {
		t.Fatalf("result summary = %q, want %q", res.Summary, want)
	}
	if got := res.Payload["subtype"]; got != "error_max_turns" {
		t.Fatalf("result payload subtype = %v, want error_max_turns", got)
	}
	if got := res.Payload["is_error"]; got != true {
		t.Fatalf("result payload is_error = %v, want true", got)
	}
	if got := res.Payload["input_tokens"]; got != 54120 {
		t.Fatalf("result payload input_tokens = %v, want 54120", got)
	}
	if got := res.Payload["cost_usd"]; got != 0.4312 {
		t.Fatalf("result payload cost_usd = %v, want 0.4312", got)
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
	if len(steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(steps))
	}
	out, _ := steps[2].Payload["result"].(string)
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

// Real claude-code 2.1.x logs (recorded 2026-10-05 on a fictional task in
// C:\work\job, sanitized; see PR 4 review).

func TestClaudeRealVerify(t *testing.T) {
	p := NewClaudeParser("3f2504e0-4f89-41d3-9a0c-0305e82c3301")
	feedFile(t, p, "testdata/claude_real_verify.jsonl")
	if p.SessionID() != "3f2504e0-4f89-41d3-9a0c-0305e82c3301" {
		t.Fatalf("session = %q", p.SessionID())
	}
	types := map[string]int{}
	for _, s := range p.Steps() {
		types[s.Type]++
	}
	for _, want := range []string{"reasoning", "command", "file", "tool", "tool_result", "result"} {
		if types[want] == 0 {
			t.Fatalf("no %q step; types = %v", want, types)
		}
	}
	if _, failed := p.Failed(); failed {
		t.Fatal("Failed = true on a success log")
	}
	u := p.Usage()
	if u.InputTokens <= 0 || u.OutputTokens <= 0 {
		t.Fatalf("usage = %+v", u)
	}
	if u.CostUSD < 0.23 || u.CostUSD > 0.24 {
		t.Fatalf("cost = %v", u.CostUSD)
	}
}

func TestClaudeRealResume(t *testing.T) {
	p := NewClaudeParser("3f2504e0-4f89-41d3-9a0c-0305e82c3301")
	feedFile(t, p, "testdata/claude_real_resume.jsonl")
	if p.SessionID() != "3f2504e0-4f89-41d3-9a0c-0305e82c3301" {
		t.Fatalf("session = %q", p.SessionID())
	}
	if _, failed := p.Failed(); failed {
		t.Fatal("Failed = true on a success resume log")
	}
}

func TestClaudeRealBadSession(t *testing.T) {
	p := NewClaudeParser("99999999-9999-4999-8999-999999999999")
	feedFile(t, p, "testdata/claude_real_bad_session.jsonl")
	msg, failed := p.Failed()
	if !failed || !strings.Contains(msg, "No conversation found") {
		t.Fatalf("Failed = %v %q", failed, msg)
	}
}
