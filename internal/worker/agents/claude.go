package agents

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// claudeParser parses `claude -p --output-format stream-json --verbose`
// JSONL (05-worker.md).
type claudeParser struct {
	expect   string
	session  string
	steps    []Step
	byToolID map[string]int // tool_use_id -> index in steps
	usage    Usage
	failed   string
	hasFail  bool
}

// NewClaudeParser creates a parser for claude stream-json output.
// expectSession is the session id the worker generated; "" disables the
// match check (the first init session is simply remembered).
func NewClaudeParser(expectSession string) Parser {
	return &claudeParser{expect: expectSession, byToolID: make(map[string]int)}
}

// claudeMessage is the message object of assistant/user events.
type claudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// claudeInput holds the tool_use input fields the parser cares about.
type claudeInput struct {
	Command      string `json:"command"`
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Query        string `json:"query"`
	URL          string `json:"url"`
}

// claudeEvent is one line of claude stream-json output; unknown fields and
// event types are ignored.
type claudeEvent struct {
	Type      string         `json:"type"`
	Subtype   string         `json:"subtype"`
	SessionID string         `json:"session_id"`
	Message   *claudeMessage `json:"message"`
	IsError   bool           `json:"is_error"`
	Result    string         `json:"result"`
	// TotalCostUSD is the run cost; usage carries the token counters.
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        *struct {
		InputTokens          int `json:"input_tokens"`
		CacheReadInputTokens int `json:"cache_read_input_tokens"`
		OutputTokens         int `json:"output_tokens"`
	} `json:"usage"`
}

// claudeBlock is one content block of an assistant/user message.
type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     *claudeInput    `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // tool_result: string or []block
	IsError   bool            `json:"is_error"`
}

// Feed implements Parser.
func (p *claudeParser) Feed(line []byte, ts time.Time) {
	var e claudeEvent
	if err := json.Unmarshal(line, &e); err != nil {
		return
	}
	switch e.Type {
	case "system":
		if e.Subtype == "init" {
			p.init(e)
		}
	case "assistant":
		p.assistant(e, ts)
	case "user":
		p.user(e, ts)
	case "result":
		p.result(e)
	}
}

// init records the session id and checks it against the expected one.
func (p *claudeParser) init(e claudeEvent) {
	if p.expect != "" && e.SessionID != p.expect {
		p.fail(fmt.Sprintf("claude session mismatch: expected %s, got %s", p.expect, e.SessionID))
		return
	}
	p.session = e.SessionID
}

// assistant maps assistant content blocks to trace steps.
func (p *claudeParser) assistant(e claudeEvent, ts time.Time) {
	for _, b := range contentBlocks(e.Message) {
		step := Step{Ts: ts}
		switch b.Type {
		case "text":
			step.Type = "message"
			step.Summary = headRunes(b.Text, summaryRunes)
		case "thinking":
			step.Type = "reasoning"
			step.Summary = headRunes(b.Thinking, summaryRunes)
		case "tool_use":
			step = p.toolUse(b, ts)
		default:
			continue
		}
		p.steps = append(p.steps, step)
		if b.Type == "tool_use" && b.ID != "" {
			p.byToolID[b.ID] = len(p.steps) - 1
		}
	}
}

// toolUse maps a tool_use block to a step by tool name.
func (p *claudeParser) toolUse(b claudeBlock, ts time.Time) Step {
	step := Step{Ts: ts, ToolUseID: b.ID}
	var input claudeInput
	if b.Input != nil {
		input = *b.Input
	}
	switch b.Name {
	case "Bash":
		step.Type = "command"
		step.Summary = headRunes(input.Command, summaryRunes)
		step.Payload = map[string]interface{}{"command": input.Command}
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		path := input.FilePath
		if path == "" {
			path = input.NotebookPath
		}
		step.Type = "file"
		step.Summary = headRunes(path, summaryRunes)
		step.Payload = map[string]interface{}{"path": path}
	case "WebSearch":
		step.Type = "web"
		step.Summary = headRunes(input.Query, summaryRunes)
	case "WebFetch":
		step.Type = "web"
		step.Summary = headRunes(input.URL, summaryRunes)
	default:
		step.Type = "tool"
		step.Summary = headRunes(b.Name, summaryRunes)
	}
	return step
}

// user attaches tool_result blocks to their tool_use steps; a failed result
// also produces an error step.
func (p *claudeParser) user(e claudeEvent, ts time.Time) {
	for _, b := range contentBlocks(e.Message) {
		if b.Type != "tool_result" {
			continue
		}
		text := tailBytes(toolResultText(b.Content), outputTailBytes)
		if idx, ok := p.byToolID[b.ToolUseID]; ok {
			if p.steps[idx].Payload == nil {
				p.steps[idx].Payload = make(map[string]interface{})
			}
			p.steps[idx].Payload["result"] = text
		}
		if b.IsError {
			p.steps = append(p.steps, Step{
				Ts:      ts,
				Type:    "error",
				Summary: headRunes(text, summaryRunes),
				Payload: map[string]interface{}{"text": text},
			})
		}
	}
}

// result folds the final result event into usage, cost and failure.
func (p *claudeParser) result(e claudeEvent) {
	if e.Usage != nil {
		p.usage.InputTokens += e.Usage.InputTokens
		p.usage.CachedInputTokens += e.Usage.CacheReadInputTokens
		p.usage.OutputTokens += e.Usage.OutputTokens
	}
	p.usage.CostUSD = e.TotalCostUSD
	if e.IsError || (e.Subtype != "" && e.Subtype != "success") {
		msg := e.Result
		if msg == "" {
			msg = e.Subtype
		}
		if msg == "" {
			msg = "claude run failed"
		}
		p.fail(msg)
	}
}

// contentBlocks decodes a message content array; a plain string content
// (seen on some user events) yields no blocks.
func contentBlocks(msg *claudeMessage) []claudeBlock {
	if msg == nil || len(msg.Content) == 0 {
		return nil
	}
	var blocks []claudeBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil
	}
	return blocks
}

// toolResultText extracts text from a tool_result content: either a plain
// string or an array of text blocks glued together.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func (p *claudeParser) fail(msg string) {
	if !p.hasFail {
		p.failed = msg
		p.hasFail = true
	}
}

// Steps implements Parser.
func (p *claudeParser) Steps() []Step { return p.steps }

// ThreadID implements Parser: claude has no thread id.
func (p *claudeParser) ThreadID() string { return "" }

// SessionID implements Parser.
func (p *claudeParser) SessionID() string { return p.session }

// Usage implements Parser.
func (p *claudeParser) Usage() Usage { return p.usage }

// Failed implements Parser.
func (p *claudeParser) Failed() (string, bool) { return p.failed, p.hasFail }
