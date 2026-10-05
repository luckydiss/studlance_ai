package procwin

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestHelperProcess sleeps for a minute when re-executed as a helper.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	time.Sleep(60 * time.Second)
	os.Exit(0)
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

	// A second Kill must not panic.
	g.Kill()
}

func TestGroupKillWithoutAdd(t *testing.T) {
	g := NewGroup()
	g.Kill()
	g.Kill()
}
