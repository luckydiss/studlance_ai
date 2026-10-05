package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Worker is the studlance-worker configuration read from worker.toml.
type Worker struct {
	ServerURL string `toml:"server_url"`
	Token     string `toml:"token"`
	Name      string `toml:"name"`
	WorkDir   string `toml:"work_dir"`
	PdfToppm  string `toml:"pdftoppm"`

	Codex  WorkerCommand `toml:"codex"`
	Claude WorkerCommand `toml:"claude"`

	Timeouts     WorkerTimeouts     `toml:"timeouts"`
	Capabilities WorkerCapabilities `toml:"capabilities"`
}

// WorkerCommand is a CLI invocation (command plus extra args).
type WorkerCommand struct {
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
}

// WorkerTimeouts holds stage and heartbeat durations, TOML as strings.
type WorkerTimeouts struct {
	Stage     string `toml:"stage"`
	Heartbeat string `toml:"heartbeat"`
}

// WorkerCapabilities adjusts auto-detected capabilities.
type WorkerCapabilities struct {
	Extra   []string `toml:"extra"`
	Disable []string `toml:"disable"`
}

// StageDuration returns the parsed stage timeout (default 3h).
func (w Worker) StageDuration() time.Duration { return parseDuration(w.Timeouts.Stage, 3*time.Hour) }

// HeartbeatDuration returns the parsed heartbeat interval (default 15s).
func (w Worker) HeartbeatDuration() time.Duration {
	return parseDuration(w.Timeouts.Heartbeat, 15*time.Second)
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

// DefaultWorkerConfigPath returns the config path: --config flag args win,
// otherwise worker.toml next to the executable.
func DefaultWorkerConfigPath(args []string, execPath string) (string, []string, error) {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to worker.toml")
	if err := fs.Parse(args); err != nil {
		return "", nil, err
	}
	if *cfgPath != "" {
		return *cfgPath, fs.Args(), nil
	}
	dir := filepath.Dir(execPath)
	return filepath.Join(dir, "worker.toml"), fs.Args(), nil
}

// LoadWorker reads worker.toml from path and validates required fields.
func LoadWorker(path string) (Worker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Worker{}, fmt.Errorf("read %s: %w", path, err)
	}
	var w Worker
	if err := toml.Unmarshal(data, &w); err != nil {
		return Worker{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if w.ServerURL == "" {
		return Worker{}, errors.New("worker.toml: server_url is required")
	}
	if w.Name == "" {
		return Worker{}, errors.New("worker.toml: name is required")
	}
	if w.WorkDir == "" {
		return Worker{}, errors.New("worker.toml: work_dir is required")
	}
	abs, err := filepath.Abs(w.WorkDir)
	if err != nil {
		return Worker{}, fmt.Errorf("worker.toml: work_dir: %w", err)
	}
	w.WorkDir = abs
	if w.Codex.Command == "" {
		w.Codex.Command = "codex"
	}
	if w.Claude.Command == "" {
		w.Claude.Command = "claude"
	}
	return w, nil
}

// LoadWorkerSoft reads worker.toml if it exists, without requiring fields —
// for probe, which also works with bare defaults (codex/claude from PATH).
func LoadWorkerSoft(path string) Worker {
	var w Worker
	if data, err := os.ReadFile(path); err == nil {
		_ = toml.Unmarshal(data, &w)
	}
	if w.Codex.Command == "" {
		w.Codex.Command = "codex"
	}
	if w.Claude.Command == "" {
		w.Claude.Command = "claude"
	}
	return w
}
