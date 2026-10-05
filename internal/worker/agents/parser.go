// Package agents runs coding CLI agents (codex, claude) and parses their
// JSONL stdout into trace steps, usage counters and failure reasons
// (05-worker.md, "Запуск агента и трейс").
//
// Parsers tolerate unknown events, unknown fields and broken lines: anything
// unrecognized is ignored (it stays in the raw log). Step summaries are cut
// at 300 runes; command and tool outputs keep only the last 20 KB.
package agents

import (
	"fmt"
	"strconv"
	"time"
)

const (
	// summaryRunes is the maximum length of a Step.Summary, in runes.
	summaryRunes = 300
	// outputTailBytes is how many bytes of a command/tool output are kept
	// (the tail, which usually carries the error).
	outputTailBytes = 20 * 1024
)

// Step is one parsed trace step of an agent run.
type Step struct {
	Ts      time.Time
	Type    string // message|reasoning|command|file|web|tool|tool_result|result|error
	Summary string
	Payload map[string]interface{}
}

// Usage accumulates token counters and cost of an agent run.
type Usage struct {
	InputTokens       int
	CachedInputTokens int
	OutputTokens      int
	CostUSD           float64
}

// Parser consumes raw JSONL lines of one CLI and accumulates the trace.
type Parser interface {
	// Feed consumes one stdout line; unknown or broken lines are ignored,
	// Feed never reports errors. Steps are immutable once appended: a
	// tool_result arrives as its own step, never by mutating an earlier one.
	Feed(line []byte, ts time.Time)
	// Steps returns every step parsed so far, in arrival order.
	Steps() []Step
	// ThreadID is the codex thread id (thread.started), "" until known.
	ThreadID() string
	// SessionID is the claude session id (system init), "" until known or
	// when it does not match the expected session.
	SessionID() string
	// Usage returns the accumulated token/cost counters.
	Usage() Usage
	// Failed reports the agent-reported failure (codex turn.failed, claude
	// result is_error or subtype != "success"). Codex "error" events are
	// reconnect warnings and do not fail the run.
	Failed() (string, bool)
}

// headRunes returns the first n runes of s.
func headRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// tailBytes returns the last n bytes of s, cut at a rune boundary so the
// result stays valid UTF-8.
func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && s[0]&0xC0 == 0x80 {
		s = s[1:]
	}
	return s
}

// humanTokens renders a token count compactly: 12300 -> "12.3k".
func humanTokens(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// noteTrunc adds the "_truncated" note of a compacted raw line (see
// truncateJSONLine) to a step payload; payload may be nil.
func noteTrunc(payload map[string]interface{}, truncated string) map[string]interface{} {
	if truncated == "" {
		return payload
	}
	if payload == nil {
		payload = make(map[string]interface{})
	}
	payload["_truncated"] = truncated
	return payload
}
