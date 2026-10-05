package jobs

import (
	"testing"

	"github.com/luckydiss/studlance_ai/internal/store"
)

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"задание.pdf", "задание.pdf", false},
		{"папка/файл.txt", "папка/файл.txt", false},
		{`папка\файл.txt`, "папка/файл.txt", false},
		{"/abs.txt", "abs.txt", false},
		{"a//b.txt", "a/b.txt", false},
		{"../x", "", true},
		{"a/../../x", "", true},
		{"a/./b", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := NormalizePath(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%q: expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%q: got %q want %q", c.in, got, c.want)
		}
	}
}

func TestTitleFromPrompt(t *testing.T) {
	got := TitleFromPrompt("Курсовая работа вариант 14 всё по методичке кафедры и ещё слова сверху")
	if got != "Курсовая работа вариант 14 всё по методичке кафедры" {
		t.Fatalf("title %q", got)
	}
}

// TestStatusStepStates verifies the done/active/pending ordering and that
// "Делаем работу" is done while verify runs.
func TestStatusStepStates(t *testing.T) {
	state := func(steps []StatusStep) []string {
		out := make([]string, 0, len(steps))
		for _, s := range steps {
			out = append(out, s.State)
		}
		return out
	}
	eq := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	verify := store.Job{Status: store.StatusRunning, Stage: store.StageVerify}
	if got := state(StatusSteps(verify)); !eq(got, []string{StepDone, StepDone, StepDone, StepActive, StepPending}) {
		t.Fatalf("verify stages %v", got)
	}

	queued := store.Job{Status: store.StatusQueued, Stage: store.StageDraft}
	if got := state(StatusSteps(queued)); !eq(got, []string{StepDone, StepDone, StepActive, StepPending, StepPending}) {
		t.Fatalf("queued stages %v", got)
	}

	done := store.Job{Status: store.StatusDone, CurrentVersion: 1}
	if got := state(StatusSteps(done)); !eq(got, []string{StepDone, StepDone, StepDone, StepDone, StepDone}) {
		t.Fatalf("done stages %v", got)
	}
}
