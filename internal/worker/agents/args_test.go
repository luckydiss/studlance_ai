package agents

import (
	"slices"
	"testing"

	"github.com/luckydiss/studlance_ai/internal/config"
)

func TestCodexArgsFresh(t *testing.T) {
	cfg := config.WorkerCommand{Command: "codex", Args: []string{"-m", "x"}}
	got := CodexArgs(cfg, `C:\work\job`, "")
	want := []string{
		"exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox",
		"-C", `C:\work\job`, "-m", "x", "-",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("CodexArgs fresh:\n got %q\nwant %q", got, want)
	}
}

func TestCodexArgsResume(t *testing.T) {
	cfg := config.WorkerCommand{Command: "codex", Args: []string{"-m", "x"}}
	got := CodexArgs(cfg, `C:\work\job`, "thread-1")
	want := []string{
		"exec", "resume", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox",
		"-m", "x", "thread-1", "-",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("CodexArgs resume:\n got %q\nwant %q", got, want)
	}
}

func TestClaudeArgsFresh(t *testing.T) {
	cfg := config.WorkerCommand{Command: "claude", Args: []string{"-m", "x"}}
	got := ClaudeArgs(cfg, "sess-1", false)
	want := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions",
		"-m", "x", "--session-id", "sess-1",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ClaudeArgs fresh:\n got %q\nwant %q", got, want)
	}
}

func TestClaudeArgsResume(t *testing.T) {
	cfg := config.WorkerCommand{Command: "claude", Args: []string{"-m", "x"}}
	got := ClaudeArgs(cfg, "sess-1", true)
	want := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions",
		"-m", "x", "--resume", "sess-1",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ClaudeArgs resume:\n got %q\nwant %q", got, want)
	}
}
