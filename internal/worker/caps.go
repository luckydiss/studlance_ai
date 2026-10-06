package worker

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/luckydiss/studlance_ai/internal/config"
)

// cliProbeTimeout bounds a single `<tool> --version` probe (05-worker.md
// «Возможности»).
const cliProbeTimeout = 10 * time.Second

// detectCapabilities collects worker capabilities: CLI agents and office
// suites that answered, plus cfg.Capabilities.Extra, minus
// cfg.Capabilities.Disable. info carries versions and host facts for
// diagnostics; everything is best-effort.
func detectCapabilities(cfg config.Worker) (caps []string, info map[string]interface{}) {
	info = map[string]interface{}{
		"os":   runtime.GOOS,
		"arch": runtime.GOARCH,
	}
	if host, err := os.Hostname(); err == nil {
		info["hostname"] = host
	}

	seen := map[string]bool{}
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			caps = append(caps, name)
		}
	}

	if v, ok := cliVersion(cfg.Codex.Command); ok {
		add("codex")
		info["codex_version"] = v
	}
	if v, ok := cliVersion(cfg.Claude.Command); ok {
		add("claude")
		info["claude_version"] = v
	}
	if hasProgID("KOMPAS.Application.7") || hasProgID("Kompas.Application.5") {
		add("kompas")
	}
	if hasProgID("Word.Application") {
		add("word")
	}
	if hasProgID("Excel.Application") {
		add("excel")
	}
	if hasProgID("PowerPoint.Application") {
		add("powerpoint")
	}
	if v, ok := cliVersion("python"); ok {
		add("python")
		info["python_version"] = v
	}
	if v, ok := cliVersion("soffice"); ok {
		add("libreoffice")
		info["libreoffice_version"] = v
	}

	for _, name := range cfg.Capabilities.Extra {
		add(name)
	}
	for _, name := range cfg.Capabilities.Disable {
		if !seen[name] {
			continue
		}
		seen[name] = false
		for i, c := range caps {
			if c == name {
				caps = append(caps[:i], caps[i+1:]...)
				break
			}
		}
	}
	return caps, info
}

// cliVersion runs `<command> --version` and returns the first output line;
// ok is false when the command is missing, fails or stays silent.
func cliVersion(command string) (v string, ok bool) {
	if command == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, "--version").Output()
	if err != nil {
		return "", false
	}
	line, _, _ := strings.Cut(string(out), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}
	return line, true
}
