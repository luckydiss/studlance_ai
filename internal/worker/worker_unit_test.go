package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luckydiss/studlance_ai/internal/httpapi"
	"github.com/luckydiss/studlance_ai/internal/worker/client"
)

// Пункт 9: имена файлов, недопустимые в Windows.
func TestMapLocalName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"отчёт.docx", "отчёт.docx"},
		{"папка/файл.pdf", "папка/файл.pdf"},
		{`отчёт: итог.docx`, "отчёт_ итог.docx"},
		{`a?b*c"d<e>f|g.txt`, "a_b_c_d_e_f_g.txt"},
		{"конец.", "конец"},
		{"конец  ", "конец"},
		{"CON", "CON_"},
		{"con.docx", "con_.docx"},
		{"COM1", "COM1_"},
		{"LPT9.txt", "LPT9_.txt"},
		{"папка/CON/файл.txt", "папка/CON_/файл.txt"},
		{"normal-name_1.xlsx", "normal-name_1.xlsx"},
	}
	for _, c := range cases {
		if got := mapLocalName(c.in); got != c.want {
			t.Errorf("mapLocalName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Пункт 6: continue/answer без известной сессии = полный промпт этапа.
func TestBuildPrompt(t *testing.T) {
	mk := func(action string, attempt int, answer *string) *jobExec {
		asn := &httpapi.Assignment{
			Action:  httpapi.AssignmentAction(action),
			Stage:   httpapi.AssignmentStageDraft,
			Attempt: attempt,
			Prompt:  "Запрос клиента",
			Answer:  answer,
			Version: 1,
		}
		j := &jobExec{asn: asn, dir: t.TempDir()}
		j.inputFiles = []httpapi.WorkerInputFile{{Path: "задание.txt", Size: 100}}
		return j
	}

	// start, attempt 0: промпт draft без блока повтора.
	p, err := mk("start", 0, nil).buildPrompt("draft", "start", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "Запрос клиента") || !strings.Contains(p, "Ты выполняешь студенческую работу") {
		t.Fatalf("start: нет запроса/общего блока")
	}
	if strings.Contains(p, "Предыдущая попытка") {
		t.Fatalf("start attempt 0: не должно быть блока повтора")
	}

	// start, attempt 1: блок повтора есть.
	p, _ = mk("start", 1, nil).buildPrompt("draft", "start", false)
	if !strings.Contains(p, "Предыдущая попытка не завершилась") {
		t.Fatalf("start attempt 1: нет блока повтора")
	}

	// continue с сессией: короткий промпт continue.
	p, _ = mk("continue", 0, nil).buildPrompt("draft", "continue", true)
	if strings.Contains(p, "Запрос клиента") || !strings.Contains(p, "Работа была прервана") {
		t.Fatalf("continue+resume: не тот промпт:\n%s", p)
	}

	// continue без сессии: полный промпт этапа + блок повтора.
	p, _ = mk("continue", 0, nil).buildPrompt("draft", "continue", false)
	if !strings.Contains(p, "Запрос клиента") || !strings.Contains(p, "Ты выполняешь студенческую работу") ||
		!strings.Contains(p, "Предыдущая попытка не завершилась") {
		t.Fatalf("continue без сессии: нет полного промпта:\n%s", p)
	}

	// answer с сессией: только ответ.
	ans := "Вариант 14"
	p, _ = mk("answer", 0, &ans).buildPrompt("draft", "answer", true)
	if !strings.Contains(p, "Студент ответил на твой вопрос:\nВариант 14") || strings.Contains(p, "Предыдущая попытка") {
		t.Fatalf("answer+resume: не тот промпт:\n%s", p)
	}

	// answer без сессии: полный промпт этапа + ответ в конце.
	p, _ = mk("answer", 0, &ans).buildPrompt("draft", "answer", false)
	if !strings.Contains(p, "Запрос клиента") || !strings.Contains(p, "Предыдущая попытка не завершилась") ||
		!strings.Contains(p, "Студент ответил на вопрос: Вариант 14") {
		t.Fatalf("answer без сессии: нет ответа/полного промпта:\n%s", p)
	}
}

// Пункт 14: summaryTitle — BOM, все ведущие «#» и пробелы.
func TestSummaryTitle(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "SUMMARY.md")
	cases := []struct{ in, want string }{
		{"# Заголовок\nтекст", "Заголовок"},
		{"###  Заголовок два\nтекст", "Заголовок два"},
		{"\ufeff# Заголовок с BOM", "Заголовок с BOM"},
		{"Просто строка", "Просто строка"},
		{"#", ""},
	}
	for _, c := range cases {
		if err := os.WriteFile(p, []byte(c.in), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := summaryTitle(p); got != c.want {
			t.Errorf("summaryTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := summaryTitle(filepath.Join(dir, "нет.md")); got != "" {
		t.Errorf("missing file: %q", got)
	}
}

// Пункт 4: загрузка повторяется при обрывах соединения.
func TestPutWithRetry(t *testing.T) {
	old := uploadBackoffInitial
	uploadBackoffInitial = time.Millisecond
	defer func() { uploadBackoffInitial = old }()
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) <= 2 {
			// Обрыв соединения без ответа.
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := filepath.Join(dir, "f.bin")
	if err := os.WriteFile(f, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	cl := client.New(srv.URL, "tok")
	j := &jobExec{w: &Worker{cl: cl}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	err := j.putWithRetry(context.Background(), f, func(r io.Reader) error {
		return cl.PutSnapshotFile(context.Background(), "j1", "draft", 1, "f.bin", r)
	})
	if err != nil {
		t.Fatalf("putWithRetry: %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
}

// Пункт 4: скачивание повторяется при битой sha256.
func TestDownloadInputRetry(t *testing.T) {
	old := uploadBackoffInitial
	uploadBackoffInitial = time.Millisecond
	defer func() { uploadBackoffInitial = old }()
	good := []byte("правильные байты")
	goodSha := sha256.Sum256(good)
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			_, _ = w.Write([]byte("битые байты"))
			return
		}
		_, _ = w.Write(good)
	}))
	defer srv.Close()

	cl := client.New(srv.URL, "tok")
	w := &Worker{cl: cl, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	asn := &httpapi.Assignment{JobId: "j1", Epoch: 1}
	j := &jobExec{w: w, asn: asn, dir: t.TempDir(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := os.MkdirAll(filepath.Join(j.dir, "input"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := j.downloadInput(context.Background(), httpapi.WorkerInputFile{
		Path: "файл.txt", Size: len(good), Sha256: hex.EncodeToString(goodSha[:]),
	})
	if err != nil {
		t.Fatalf("downloadInput: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(j.dir, "input", "файл.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(good) || attempts.Load() != 2 {
		t.Fatalf("got %q attempts %d", got, attempts.Load())
	}
}

// Пункт 4: stale lease при загрузке не повторяется.
func TestPutWithRetryNoStale(t *testing.T) {
	old := uploadBackoffInitial
	uploadBackoffInitial = time.Millisecond
	defer func() { uploadBackoffInitial = old }()
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"stale_lease","message":"x"}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := filepath.Join(dir, "f.bin")
	_ = os.WriteFile(f, []byte("data"), 0o644)
	cl := client.New(srv.URL, "tok")
	j := &jobExec{w: &Worker{cl: cl}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	err := j.putWithRetry(context.Background(), f, func(r io.Reader) error {
		return cl.PutSnapshotFile(context.Background(), "j1", "draft", 1, "f.bin", r)
	})
	if !errors.Is(err, client.ErrStaleLease) || attempts.Load() != 1 {
		t.Fatalf("err = %v, attempts = %d, want stale_lease after 1 attempt", err, attempts.Load())
	}
}
