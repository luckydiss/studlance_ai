package worker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/luckydiss/studlance_ai/internal/config"
)

// fakeCodex/fakeClaude are built by TestMain.
var fakeCodex, fakeClaude string

// TestMain answers the cliVersion probe: re-executed with exactly one
// --version arg (before the testing flags are parsed), it prints a version.
// It also builds the fakeagent binaries (named codex/claude) for flow tests.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("studlance-test-cli 0.0.1")
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "fakeagent-build")
	if err != nil {
		fmt.Fprintln(os.Stderr, "flow tests: mktemp:", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	bin := filepath.Join(dir, "fakeagent")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/fakeagent").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "flow tests: build fakeagent: %v\n%s\n", err, out)
		os.Exit(1)
	}
	raw, err := os.ReadFile(bin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "flow tests:", err)
		os.Exit(1)
	}
	for _, name := range []string{"codex", "claude"} {
		p := filepath.Join(dir, name)
		if runtime.GOOS == "windows" {
			p += ".exe"
		}
		if err := os.WriteFile(p, raw, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "flow tests:", err)
			os.Exit(1)
		}
		if name == "codex" {
			fakeCodex = p
		} else {
			fakeClaude = p
		}
	}
	os.Exit(m.Run())
}

func TestDetectCapabilitiesCLI(t *testing.T) {
	var cfg config.Worker
	cfg.Codex.Command = os.Args[0]

	caps, info := detectCapabilities(cfg)
	if !slices.Contains(caps, "codex") {
		t.Fatalf("caps %v missing codex", caps)
	}
	if info["codex_version"] != "studlance-test-cli 0.0.1" {
		t.Fatalf("codex_version = %v", info["codex_version"])
	}
	if info["os"] == "" || info["arch"] == "" || info["hostname"] == "" {
		t.Fatalf("info missing host facts: %v", info)
	}
}

func TestDetectCapabilitiesMissingCLI(t *testing.T) {
	var cfg config.Worker
	cfg.Codex.Command = "studlance-no-such-binary-xyz"
	cfg.Claude.Command = ""

	caps, _ := detectCapabilities(cfg)
	if slices.Contains(caps, "codex") || slices.Contains(caps, "claude") {
		t.Fatalf("caps %v must not contain codex/claude", caps)
	}
}

func TestDetectCapabilitiesExtraAndDisable(t *testing.T) {
	var cfg config.Worker
	cfg.Codex.Command = os.Args[0]
	cfg.Capabilities.Extra = []string{"custom-tool"}
	cfg.Capabilities.Disable = []string{"codex", "python"}

	caps, _ := detectCapabilities(cfg)
	if !slices.Contains(caps, "custom-tool") {
		t.Fatalf("caps %v missing extra", caps)
	}
	if slices.Contains(caps, "codex") || slices.Contains(caps, "python") {
		t.Fatalf("caps %v must be minus disabled", caps)
	}
}
