package prompts_test

import (
	"strings"
	"testing"

	"github.com/luckydiss/studlance_ai/prompts"
)

func TestRenderAll(t *testing.T) {
	v := prompts.Vars{
		Prompt:       "Сделай курсовую",
		Files:        "- input/задание.pdf (1.2 МБ)",
		Version:      2,
		PrevVersion:  1,
		Answer:       "Вариант 14",
		RevisionFile: "REVISION-2.md",
		Retry:        true,
	}
	for _, name := range []string{"draft", "verify", "revise", "revise_full", "answer", "continue"} {
		out, err := prompts.Render(name, v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(out, "{{") {
			t.Fatalf("%s: unresolved template action in output", name)
		}
	}
}

func TestRenderVars(t *testing.T) {
	d, err := prompts.Render("draft", prompts.Vars{Prompt: "Запрос", Files: "- input/a.pdf (1 КБ)"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "Запрос") || !strings.Contains(d, "Ты выполняешь студенческую работу") {
		t.Fatal("draft missing common block or prompt")
	}
	if strings.Contains(d, "Предыдущая попытка") {
		t.Fatal("retry block must be absent without Retry")
	}
	r, err := prompts.Render("revise", prompts.Vars{Version: 2, PrevVersion: 1, RevisionFile: "REVISION-2.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r, "версию 1") || !strings.Contains(r, "input/revision-2/") {
		t.Fatal("revise missing version vars")
	}
	a, err := prompts.Render("answer", prompts.Vars{Answer: "Вариант 14"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a, "Вариант 14") {
		t.Fatal("answer missing answer text")
	}

	// The full revise prompt (no claude session) carries the common block,
	// the client request and the local file names; verify does too.
	full := prompts.Vars{Prompt: "Запрос", Files: "- input/a.pdf (1 КБ)", Version: 2, PrevVersion: 1, RevisionFile: "REVISION-2.md"}
	rf, err := prompts.Render("revise_full", full)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Ты выполняешь студенческую работу", "Запрос", "- input/a.pdf (1 КБ)", "REVISION-2.md"} {
		if !strings.Contains(rf, want) {
			t.Fatalf("revise_full missing %q:\n%s", want, rf)
		}
	}
	v, err := prompts.Render("verify", full)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Запрос", "- input/a.pdf (1 КБ)"} {
		if !strings.Contains(v, want) {
			t.Fatalf("verify missing %q:\n%s", want, v)
		}
	}
}
