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
// Второй раунд, п.5: таблица на все этапы, действия и наличие/отсутствие
// сессии — полный промпт (общий блок, запрос клиента, локальные имена файлов,
// инструкции этапа) или короткий диалоговый.
func TestBuildPromptTable(t *testing.T) {
	ans := "Вариант 14"
	const (
		commonMarker  = "Ты выполняешь студенческую работу"
		request       = "Запрос клиента"
		fileLine      = "- input/задание.txt (100 Б)"
		retryMarker   = "Предыдущая попытка не завершилась"
		shortContinue = "Работа была прервана"
	)

	cases := []struct {
		name         string
		stage        string
		action       string
		resume       bool
		attempt      int
		full         bool
		retry        bool
		answerSuffix bool
		short        string // "continue" | "answer" | "revise" | ""
	}{
		{name: "start draft attempt0", stage: "draft", action: "start", full: true},
		{name: "start draft attempt1", stage: "draft", action: "start", attempt: 1, full: true, retry: true},
		{name: "start verify attempt1", stage: "verify", action: "start", attempt: 1, full: true, retry: true},
		{name: "start revise", stage: "revise", action: "start", full: true},
		{name: "continue resume", stage: "draft", action: "continue", resume: true, short: "continue"},
		{name: "continue no session draft", stage: "draft", action: "continue", full: true, retry: true},
		{name: "continue no session verify", stage: "verify", action: "continue", full: true, retry: true},
		{name: "continue no session revise", stage: "revise", action: "continue", full: true, retry: true},
		{name: "answer resume", stage: "verify", action: "answer", resume: true, short: "answer"},
		{name: "answer no session draft", stage: "draft", action: "answer", full: true, retry: true, answerSuffix: true},
		{name: "answer no session verify", stage: "verify", action: "answer", full: true, retry: true, answerSuffix: true},
		{name: "answer no session revise", stage: "revise", action: "answer", full: true, retry: true, answerSuffix: true},
		{name: "revise resume", stage: "revise", action: "revise", resume: true, short: "revise"},
		{name: "revise no session", stage: "revise", action: "revise", full: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asn := &httpapi.Assignment{
				Action:  httpapi.AssignmentAction(tc.action),
				Stage:   httpapi.AssignmentStage(tc.stage),
				Attempt: tc.attempt,
				Prompt:  request,
				Answer:  &ans,
				Version: 2,
			}
			j := &jobExec{asn: asn, dir: t.TempDir()}
			j.inputFiles = []httpapi.WorkerInputFile{{Path: "задание.txt", Size: 100}}

			p, err := j.buildPrompt(tc.stage, tc.action, tc.resume)
			if err != nil {
				t.Fatal(err)
			}

			hasCommon := strings.Contains(p, commonMarker)
			hasRequest := strings.Contains(p, request)
			hasFiles := strings.Contains(p, fileLine)
			if tc.full {
				if !hasCommon || !hasRequest || !hasFiles {
					t.Fatalf("full prompt is missing the common block/request/files:\n%s", p)
				}
				switch tc.stage {
				case "draft":
					if !strings.Contains(p, "Сначала изучи все материалы") {
						t.Fatalf("draft instructions missing:\n%s", p)
					}
				case "verify":
					if !strings.Contains(p, "Твоя задача — довести работу до сдачи") {
						t.Fatalf("verify instructions missing:\n%s", p)
					}
				case "revise":
					if !strings.Contains(p, "Студент посмотрел версию 1") || !strings.Contains(p, "input/revision-2/") {
						t.Fatalf("revise instructions missing:\n%s", p)
					}
				}
			} else if hasCommon || hasRequest || hasFiles {
				t.Fatalf("short prompt must not repeat the common block/request/files:\n%s", p)
			}

			if got := strings.Contains(p, retryMarker); got != tc.retry {
				t.Fatalf("retry block present = %v, want %v:\n%s", got, tc.retry, p)
			}

			trimmed := strings.TrimRight(p, "\n")
			if tc.answerSuffix {
				want := "Студент ответил на вопрос: " + ans
				if !strings.HasSuffix(trimmed, want) {
					t.Fatalf("answer prompt must end with %q:\n%s", want, p)
				}
			} else if strings.Contains(p, "Студент ответил на вопрос:") {
				t.Fatalf("unexpected answer suffix:\n%s", p)
			}

			switch tc.short {
			case "continue":
				if !strings.Contains(p, shortContinue) {
					t.Fatalf("short continue prompt expected:\n%s", p)
				}
			case "answer":
				if !strings.Contains(p, "Студент ответил на твой вопрос:\n"+ans) {
					t.Fatalf("short answer prompt expected:\n%s", p)
				}
			case "revise":
				if !strings.Contains(p, "Студент посмотрел версию 1") || !strings.Contains(p, "REVISION-2.md") {
					t.Fatalf("short revise prompt expected:\n%s", p)
				}
			}
		})
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
