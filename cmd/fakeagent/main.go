// Command fakeagent is a test double for the codex and claude CLIs.
//
// One binary stands in for both agents in worker end-to-end tests: the
// mode comes from the first argument ("codex" or "claude") or, when the
// binary is invoked under the real CLI name, is guessed from the flags.
// It reads the prompt from stdin, emits the JSONL event stream of the
// corresponding CLI on stdout, and produces the workspace artifacts
// (out/, preview/, manifest.json, SUMMARY.md, verification files) that
// the worker expects.
//
// Behavior is driven by markers in the prompt text ("#ask", "#hang", ...)
// and by the FAKEAGENT_SCRIPT environment variable holding a JSON object
// like {"marker": "#ask", "sleep_ms": 500}.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// lineDelayMs is the pause between JSONL lines, imitating a streaming CLI.
const lineDelayMs = 30

// knownMarkers are the behavior markers recognized in the prompt text.
var knownMarkers = []string{
	"#ask", "#fail-draft", "#fail-verify", "#fail-once", "#hang", "#slow", "#no-pdf",
}

// script is the parsed FAKEAGENT_SCRIPT environment variable.
type script struct {
	Marker  string `json:"marker"`
	SleepMs int    `json:"sleep_ms"`
}

// config is the parsed invocation.
type config struct {
	mode      string // "codex" or "claude"
	resume    bool
	sessionID string
	prompt    string
	markers   map[string]bool
	sleepMs   int
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// `--version` answers like the real CLI so probe/capability detection works;
	// the variant comes from the binary name (codex.exe / claude).
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-V" || args[0] == "version") {
		name := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
		switch name {
		case "codex":
			fmt.Println("codex-cli 0.160.0 (fakeagent)")
		case "claude":
			fmt.Println("2.1.0 (Claude Code, fakeagent)")
		default:
			fmt.Println("fakeagent 1.0")
		}
		return 0
	}
	cfg, err := parseConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 2
	}
	switch cfg.mode {
	case "codex":
		return runCodex(cfg)
	case "claude":
		return runClaude(cfg)
	default:
		fmt.Fprintln(os.Stderr, "fakeagent: cannot determine mode from arguments")
		return 2
	}
}

// parseConfig detects the mode, parses session arguments, reads the prompt
// from stdin and collects the behavior markers.
func parseConfig(args []string) (*config, error) {
	mode, rest := detectMode(args)
	cfg := &config{mode: mode, markers: map[string]bool{}}

	switch mode {
	case "codex":
		cfg.resume, cfg.sessionID = parseCodexArgs(rest)
	case "claude":
		cfg.resume, cfg.sessionID = parseClaudeArgs(rest)
	}

	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	cfg.prompt = string(prompt)

	// Markers live in the client's request; draft/continue/answer prompts carry
	// it verbatim, verify/revise prompts reference TASK.md instead — scan both.
	corpus := cfg.prompt
	if raw, err := os.ReadFile("TASK.md"); err == nil {
		corpus += "\n" + string(raw)
	}
	for _, m := range knownMarkers {
		if strings.Contains(corpus, m) {
			cfg.markers[m] = true
		}
	}
	if raw := os.Getenv("FAKEAGENT_SCRIPT"); raw != "" {
		var sc script
		if err := json.Unmarshal([]byte(raw), &sc); err != nil {
			return nil, fmt.Errorf("parse FAKEAGENT_SCRIPT: %w", err)
		}
		if sc.Marker != "" {
			cfg.markers[sc.Marker] = true
		}
		cfg.sleepMs = sc.SleepMs
	}
	return cfg, nil
}

// detectMode returns the agent mode and the remaining arguments. The first
// argument "codex"/"claude" wins; otherwise the mode is guessed from the
// flags the worker passes to the real CLIs.
func detectMode(args []string) (string, []string) {
	if len(args) > 0 && (args[0] == "codex" || args[0] == "claude") {
		return args[0], args[1:]
	}
	for _, a := range args {
		if a == "exec" {
			return "codex", args
		}
	}
	for _, a := range args {
		if a == "-p" || a == "--output-format" {
			return "claude", args
		}
	}
	return "", args
}

// parseCodexArgs extracts the resume flag and the thread id (the first
// non-flag argument after "resume").
func parseCodexArgs(args []string) (resume bool, threadID string) {
	for i, a := range args {
		if a != "resume" {
			continue
		}
		resume = true
		for _, b := range args[i+1:] {
			if !strings.HasPrefix(b, "-") {
				return true, b
			}
		}
	}
	return resume, ""
}

// parseClaudeArgs extracts the session id from --session-id (new session)
// or --resume (existing session).
func parseClaudeArgs(args []string) (resume bool, sessionID string) {
	for i, a := range args {
		if (a == "--session-id" || a == "--resume") && i+1 < len(args) {
			return a == "--resume", args[i+1]
		}
	}
	return false, ""
}

// emitter writes JSONL events to stdout with a streaming-like delay.
type emitter struct {
	delay time.Duration
	first bool
}

func newEmitter(extraMs int) *emitter {
	return &emitter{delay: time.Duration(lineDelayMs+extraMs) * time.Millisecond, first: true}
}

func (e *emitter) emit(v any) {
	if !e.first {
		time.Sleep(e.delay)
	}
	e.first = false
	data, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent: marshal event:", err)
		return
	}
	if _, err := os.Stdout.Write(append(data, '\n')); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent: write event:", err)
	}
}

// stateDir is the per-workspace folder for fakeagent's own bookkeeping.
func stateDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".fakeagent"), nil
}

// flagExists reports whether a one-shot marker flag was already recorded.
func flagExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// setFlag records a one-shot marker flag.
func setFlag(dir, name string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(time.Now().Format(time.RFC3339)), 0o644)
}

// hang imitates a stuck agent: records its PID and sleeps until killed.
func hang() int {
	dir, err := stateDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 2
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 2
	}
	if err := os.WriteFile(filepath.Join(dir, "hang.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		return 2
	}
	for {
		time.Sleep(time.Hour)
	}
}

// randHex returns n random bytes as a hex string.
func randHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "000000"
	}
	return hex.EncodeToString(buf)
}
