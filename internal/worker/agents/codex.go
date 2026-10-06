package agents

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// codexParser parses `codex exec --json` JSONL (05-worker.md).
type codexParser struct {
	steps    []Step
	threadID string
	usage    Usage
	failed   string
	hasFail  bool
}

// NewCodexParser creates a parser for codex exec --json output.
func NewCodexParser() Parser { return &codexParser{} }

// codexEvent is one line of codex exec --json output; unknown fields are
// ignored by encoding/json.
type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	// Truncated is set by the runner when the raw line was compacted before
	// parsing (see truncateJSONLine); it is propagated into step payloads.
	Truncated string `json:"_truncated"`
	Item      *struct {
		Type             string                   `json:"type"`
		Text             string                   `json:"text"`
		Command          string                   `json:"command"`
		AggregatedOutput string                   `json:"aggregated_output"`
		ExitCode         *int                     `json:"exit_code"`
		Changes          []map[string]interface{} `json:"changes"`
		Query            string                   `json:"query"`
		Server           string                   `json:"server"`
		Tool             string                   `json:"tool"`
	} `json:"item"`
	Usage *struct {
		InputTokens       int `json:"input_tokens"`
		CachedInputTokens int `json:"cached_input_tokens"`
		OutputTokens      int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Feed implements Parser.
func (p *codexParser) Feed(line []byte, ts time.Time) {
	var e codexEvent
	if err := json.Unmarshal(line, &e); err != nil {
		return
	}
	switch e.Type {
	case "thread.started":
		p.threadID = e.ThreadID
	case "item.completed":
		p.itemCompleted(e, ts)
	case "turn.completed":
		if e.Usage != nil {
			p.usage.InputTokens += e.Usage.InputTokens
			p.usage.CachedInputTokens += e.Usage.CachedInputTokens
			p.usage.OutputTokens += e.Usage.OutputTokens
			p.steps = append(p.steps, Step{
				Ts:      ts,
				Type:    "result",
				Summary: fmt.Sprintf("готово: %s токенов", humanTokens(e.Usage.InputTokens+e.Usage.OutputTokens)),
				Payload: noteTrunc(map[string]interface{}{
					"input_tokens":        e.Usage.InputTokens,
					"cached_input_tokens": e.Usage.CachedInputTokens,
					"output_tokens":       e.Usage.OutputTokens,
				}, e.Truncated),
			})
		}
	case "turn.failed":
		// Only turn.failed fails the run.
		if msg := errorMessage(e); msg != "" {
			p.fail(msg)
			p.steps = append(p.steps, Step{
				Ts:      ts,
				Type:    "error",
				Summary: headRunes(msg, summaryRunes),
				Payload: noteTrunc(nil, e.Truncated),
			})
		}
	case "error":
		// {"type":"error"} events are reconnects/warnings: they become error
		// steps but never fail the run.
		if msg := errorMessage(e); msg != "" {
			p.steps = append(p.steps, Step{
				Ts:      ts,
				Type:    "error",
				Summary: headRunes(msg, summaryRunes),
				Payload: noteTrunc(nil, e.Truncated),
			})
		}
	}
}

// errorMessage extracts the message of a turn.failed/error event.
func errorMessage(e codexEvent) string {
	if e.Error != nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return e.Message
}

// itemCompleted maps a completed codex item to a trace step.
func (p *codexParser) itemCompleted(e codexEvent, ts time.Time) {
	item := e.Item
	if item == nil {
		return
	}
	step := Step{Ts: ts}
	switch item.Type {
	case "agent_message":
		step.Type = "message"
		step.Summary = headRunes(item.Text, summaryRunes)
	case "reasoning":
		step.Type = "reasoning"
		step.Summary = headRunes(item.Text, summaryRunes)
	case "command_execution":
		step.Type = "command"
		step.Summary = headRunes(item.Command, summaryRunes)
		step.Payload = map[string]interface{}{
			"command": item.Command,
			"output":  tailBytes(item.AggregatedOutput, outputTailBytes),
		}
		if item.ExitCode != nil {
			step.Payload["exit_code"] = *item.ExitCode
		}
	case "file_change":
		step.Type = "file"
		paths := make([]string, 0, len(item.Changes))
		for _, ch := range item.Changes {
			if path, ok := ch["path"].(string); ok && path != "" {
				paths = append(paths, path)
			}
		}
		step.Summary = headRunes("изменены файлы: "+strings.Join(paths, ", "), summaryRunes)
		step.Payload = map[string]interface{}{"changes": item.Changes}
	case "web_search":
		step.Type = "web"
		step.Summary = headRunes(item.Query, summaryRunes)
	case "mcp_tool_call":
		step.Type = "tool"
		step.Summary = headRunes(item.Server+"/"+item.Tool, summaryRunes)
	default:
		return
	}
	step.Payload = noteTrunc(step.Payload, e.Truncated)
	p.steps = append(p.steps, step)
}

func (p *codexParser) fail(msg string) {
	if !p.hasFail {
		p.failed = msg
		p.hasFail = true
	}
}

// Drain implements Parser: it hands the parsed steps over and forgets them.
func (p *codexParser) Drain() []Step {
	steps := p.steps
	p.steps = nil
	return steps
}

// Steps implements Parser.
func (p *codexParser) Steps() []Step { return p.steps }

// ThreadID implements Parser.
func (p *codexParser) ThreadID() string { return p.threadID }

// SessionID implements Parser: codex has no session id.
func (p *codexParser) SessionID() string { return "" }

// Usage implements Parser.
func (p *codexParser) Usage() Usage { return p.usage }

// Failed implements Parser.
func (p *codexParser) Failed() (string, bool) { return p.failed, p.hasFail }
