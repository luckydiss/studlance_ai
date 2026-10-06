package procwin

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestHelperProcess sleeps for a minute when re-executed as a helper. With
// HELPER_MARKER set it first spawns a grandchild helper that heartbeats into
// the marker file, so tests can verify that Kill takes down the whole tree.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("GO_WANT_HELPER_PROCESS") {
	case "child":
		marker := os.Getenv("HELPER_MARKER")
		for range 1200 {
			if err := os.WriteFile(marker, []byte("tick"), 0o644); err != nil {
				os.Exit(1)
			}
			time.Sleep(50 * time.Millisecond)
		}
		os.Exit(0)
	case "1":
		if marker := os.Getenv("HELPER_MARKER"); marker != "" {
			child := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--")
			child.Env = append(os.Environ(),
				"GO_WANT_HELPER_PROCESS=child", "HELPER_MARKER="+marker)
			if err := child.Start(); err != nil {
				os.Exit(1)
			}
		}
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
}

// helperCmd re-executes the test binary as a sleeping helper process.
func helperCmd(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	return cmd
}

func TestGroupKill(t *testing.T) {
	g := NewGroup()
	cmd := helperCmd(t)
	g.Prepare(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := g.Add(cmd); err != nil {
		t.Fatal(err)
	}

	g.Kill()
	if err := cmd.Wait(); err == nil {
		t.Fatal("helper exited cleanly, want kill")
	}

	// A second Kill, and Close after Kill, must not panic.
	g.Kill()
	g.Close()
	g.Close()
}

func TestGroupKillTree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child.tick")

	g := NewGroup()
	defer g.Close()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "HELPER_MARKER="+marker)
	g.Prepare(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := g.Add(cmd); err != nil {
		t.Fatal(err)
	}

	// Wait for the grandchild to come up and start heartbeating.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("grandchild did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}

	g.Kill()
	if err := cmd.Wait(); err == nil {
		t.Fatal("helper exited cleanly, want kill")
	}

	// The heartbeat must stop: the grandchild died together with its parent.
	time.Sleep(300 * time.Millisecond)
	fi1, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	fi2, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("grandchild still heartbeating after Kill: process tree survived")
	}
}

func TestGroupKillWithoutAdd(t *testing.T) {
	g := NewGroup()
	g.Kill()
	g.Kill()
	g.Close()
}
