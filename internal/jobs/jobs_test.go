package jobs

import (
	"slices"
	"testing"
	"time"

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

func stepStates(steps []StatusStep) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.State)
	}
	return out
}

// TestStatusStepStates verifies the done/active/pending ordering: the last
// started step is active, everything before it done, everything after pending.
func TestStatusStepStates(t *testing.T) {
	now := time.Now()
	draftStarted := func(d time.Duration) *time.Time {
		ts := now.Add(-d)
		return &ts
	}

	cases := []struct {
		name string
		job  store.Job
		p    Progress
		want []string
	}{
		{
			name: "queued draft, no progress",
			job:  store.Job{Status: store.StatusQueued, Stage: store.StageDraft},
			p:    Progress{Now: now},
			want: []string{StepActive, StepPending, StepPending, StepPending, StepPending},
		},
		{
			name: "draft started 30s ago",
			job:  store.Job{Status: store.StatusRunning, Stage: store.StageDraft},
			p:    Progress{Now: now, DraftStartedAt: draftStarted(30 * time.Second)},
			want: []string{StepDone, StepActive, StepPending, StepPending, StepPending},
		},
		{
			name: "codex draft traced",
			job:  store.Job{Status: store.StatusRunning, Stage: store.StageDraft},
			p:    Progress{Now: now, CodexDraftTraced: true},
			want: []string{StepDone, StepDone, StepActive, StepPending, StepPending},
		},
		{
			name: "draft running for 2m",
			job:  store.Job{Status: store.StatusRunning, Stage: store.StageDraft},
			p:    Progress{Now: now, DraftStartedAt: draftStarted(2 * time.Minute)},
			want: []string{StepDone, StepDone, StepActive, StepPending, StepPending},
		},
		{
			name: "verify running",
			job:  store.Job{Status: store.StatusRunning, Stage: store.StageVerify},
			p:    Progress{Now: now},
			want: []string{StepDone, StepDone, StepDone, StepActive, StepPending},
		},
		{
			name: "done v1",
			job:  store.Job{Status: store.StatusDone, CurrentVersion: 1},
			p:    Progress{Now: now},
			want: []string{StepDone, StepDone, StepDone, StepDone, StepDone},
		},
	}
	for _, c := range cases {
		got := stepStates(StatusSteps(c.job, c.p))
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
