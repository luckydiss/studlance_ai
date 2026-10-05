// Package jobs holds order business rules without any HTTP concerns.
package jobs

import (
	"fmt"
	"strings"
	"time"

	"github.com/luckydiss/studlance_ai/internal/store"
)

// ClientStatus is the status shown to the client (03-lifecycle.md).
type ClientStatus string

const (
	ClientUploading  ClientStatus = "uploading"
	ClientAccepted   ClientStatus = "accepted"
	ClientInProgress ClientStatus = "in_progress"
	ClientRevising   ClientStatus = "revising"
	ClientNeedsInput ClientStatus = "needs_input"
	ClientDone       ClientStatus = "done"
	ClientDelayed    ClientStatus = "delayed"
	ClientCanceled   ClientStatus = "canceled"
)

// ClientStatusFor maps internal status/stage to the client-facing status.
// An order re-queued after a failed attempt (attempt = 1) keeps its previous
// client status ("Выполняем" / "Дорабатываем"), not "Заказ принят"
// (03-lifecycle.md).
func ClientStatusFor(j store.Job) ClientStatus {
	switch j.Status {
	case store.StatusUploading:
		return ClientUploading
	case store.StatusQueued:
		if j.Stage == store.StageRevise {
			return ClientRevising
		}
		if j.CurrentVersion == 0 && j.Attempt == 0 {
			return ClientAccepted
		}
		return ClientInProgress
	case store.StatusRunning:
		if j.Stage == store.StageRevise {
			return ClientRevising
		}
		return ClientInProgress
	case store.StatusNeedsInput:
		return ClientNeedsInput
	case store.StatusDone:
		return ClientDone
	case store.StatusFailed:
		return ClientDelayed
	case store.StatusCanceled:
		return ClientCanceled
	default:
		return ClientUploading
	}
}

// StatusText returns the Russian status text (03-lifecycle.md).
func StatusText(s ClientStatus) string {
	switch s {
	case ClientUploading:
		return "Загрузка файлов"
	case ClientAccepted:
		return "Заказ принят"
	case ClientInProgress:
		return "Выполняем"
	case ClientRevising:
		return "Дорабатываем"
	case ClientNeedsInput:
		return "Нужно уточнение"
	case ClientDone:
		return "Готово"
	case ClientDelayed:
		return "Задерживается — мы уже разбираемся"
	case ClientCanceled:
		return "Отменён"
	default:
		return ""
	}
}

// Step state values.
const (
	StepDone    = "done"
	StepActive  = "active"
	StepPending = "pending"
)

// StatusStep is one row of the client status window.
type StatusStep struct {
	Title string
	State string
}

// Progress carries the worker-driven facts StatusSteps needs (PR 3): when the
// draft stage started and whether codex already produced a trace step.
type Progress struct {
	Now              time.Time
	DraftStartedAt   *time.Time
	CodexDraftTraced bool
}

// StatusSteps builds the status window steps for a normal (non-revision) order.
// Steps before the current one are "done", the current one is "active" and the
// rest are "pending". "Делаем работу" is done once the first codex trace step
// of the draft stage arrives, or once the draft stage runs for at least a
// minute (03-lifecycle.md).
func StatusSteps(j store.Job, p Progress) []StatusStep {
	if j.Stage == store.StageRevise || j.PendingRevision != nil {
		return revisionSteps(j)
	}

	accepted := j.Status != store.StatusUploading
	draftStarted := j.Stage != "" || j.CurrentVersion > 0 || j.Status == store.StatusDone ||
		j.Status == store.StatusNeedsInput || j.Status == store.StatusFailed
	// codex finished (work done) once verify started or a version exists, or
	// once draft demonstrably produces work (first trace step / 1 minute in).
	workDone := p.CodexDraftTraced ||
		(p.DraftStartedAt != nil && !p.DraftStartedAt.After(p.Now.Add(-time.Minute))) ||
		j.CurrentVersion > 0 || j.Status == store.StatusDone ||
		(j.Status == store.StatusRunning && j.Stage == store.StageVerify)
	verifyStarted := j.CurrentVersion > 0 || j.Status == store.StatusDone
	done := j.Status == store.StatusDone

	steps := []StatusStep{
		step("Заказ принят", accepted),
		step("Разбираем задание и методичку", draftStarted),
		step("Делаем работу", workDone),
		step("Оформляем по требованиям методички", verifyStarted),
	}
	doneStep := step("Готово", done)
	if done {
		doneStep.Title = fmt.Sprintf("Готово — версия %d", j.CurrentVersion)
	}
	steps = append(steps, doneStep)
	markCurrent(steps)
	return steps
}

func revisionSteps(j store.Job) []StatusStep {
	steps := []StatusStep{
		{Title: "Получили замечания", State: StepDone},
		{Title: "Дорабатываем", State: StepActive},
	}
	last := StatusStep{Title: "Готово", State: StepPending}
	if j.Status == store.StatusDone && j.PendingRevision == nil {
		last.Title = fmt.Sprintf("Готово — версия %d", j.CurrentVersion)
		last.State = StepDone
		steps[1].State = StepDone
	}
	steps = append(steps, last)
	return steps
}

func step(title string, done bool) StatusStep {
	if done {
		return StatusStep{Title: title, State: StepDone}
	}
	return StatusStep{Title: title, State: StepPending}
}

// markCurrent marks the first pending step as active.
func markCurrent(steps []StatusStep) {
	for i := range steps {
		if steps[i].State == StepPending {
			steps[i].State = StepActive
			return
		}
	}
}

// CanCancel reports whether the order may be canceled from its current status.
func CanCancel(j store.Job) bool {
	switch j.Status {
	case store.StatusUploading, store.StatusQueued, store.StatusNeedsInput, store.StatusRunning:
		return true
	default:
		return false
	}
}

// CanRevise reports whether the client may request a rework.
func CanRevise(j store.Job) bool {
	return j.Status == store.StatusDone && j.PendingRevision == nil
}

// CanAnswer reports whether the client may answer the open question.
func CanAnswer(j store.Job) bool {
	return j.Status == store.StatusNeedsInput
}

// TitleFromPrompt builds the initial title: first 8 words, max 80 chars
// (03-lifecycle.md).
func TitleFromPrompt(prompt string) string {
	fields := strings.Fields(prompt)
	if len(fields) > 8 {
		fields = fields[:8]
	}
	title := strings.Join(fields, " ")
	if len([]rune(title)) > 80 {
		runes := []rune(title)
		title = string(runes[:80])
	}
	if title == "" {
		title = "Заказ"
	}
	return title
}

// MaxPathBytes and MaxNameBytes are the client path limits (02-data.md).
const (
	MaxPathBytes = 1024
	MaxNameBytes = 255
)

// NormalizePath validates and normalizes a client-supplied relative path
// (02-data.md): forward slashes, no "..", no leading "/", no control chars,
// name <= 255 bytes, path <= 1024 bytes.
func NormalizePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("пустой путь")
	}
	path = strings.ReplaceAll(path, "\\", "/")
	path = strings.TrimPrefix(path, "/")
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	if len(path) > MaxPathBytes {
		return "", fmt.Errorf("путь слишком длинный")
	}
	parts := strings.Split(path, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", fmt.Errorf("недопустимый путь")
		}
		if len(p) > MaxNameBytes {
			return "", fmt.Errorf("имя файла слишком длинное")
		}
		for _, r := range p {
			if r < 0x20 {
				return "", fmt.Errorf("недопустимое имя файла")
			}
		}
	}
	return strings.Join(parts, "/"), nil
}
