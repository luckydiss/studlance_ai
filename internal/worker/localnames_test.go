package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckydiss/studlance_ai/internal/httpapi"
	"github.com/luckydiss/studlance_ai/internal/worker/client"
)

// inputServer serves GET /api/worker/jobs/{id}/input/{path} from a map of
// server-side relative paths to bytes. It mirrors the real endpoint, which
// receives the whole path as one (percent-encoded) URL segment.
func inputServer(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esc := r.URL.EscapedPath()
		i := strings.Index(esc, "/input/")
		if i < 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		p, err := url.PathUnescape(esc[i+len("/input/"):])
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		b, ok := files[p]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// nameJob builds a jobExec whose input/ mapping persists under dir.
func nameJob(t *testing.T, dir string, srv *httptest.Server) *jobExec {
	t.Helper()
	meta := filepath.Join(dir, ".studlance")
	if err := os.MkdirAll(meta, 0o755); err != nil {
		t.Fatal(err)
	}
	cl := client.New(srv.URL, "tok")
	return &jobExec{
		w:     &Worker{cl: cl, logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		asn:   &httpapi.Assignment{JobId: "j1", Epoch: 1},
		dir:   dir,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		names: loadLocalNames(filepath.Join(meta, "localnames.json")),
	}
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Пункт 1 (второй раунд): a:b.txt и a_b.txt — два разных файла с разными
// SHA256 обязаны остаться двумя разными файлами на диске, ничего не
// перезаписывается.
func TestInputNamesKeepDifferentFiles(t *testing.T) {
	colon := []byte("файл с двоеточием в имени")
	under := []byte("другой файл с подчёркиванием")
	files := map[string][]byte{
		"a:b.txt": colon,
		"a_b.txt": under,
	}
	srv := inputServer(t, files)
	dir := t.TempDir()
	j := nameJob(t, dir, srv)

	// The production path registers every server file before downloading.
	inputs := make([]httpapi.WorkerInputFile, 0, len(files))
	for p, b := range files {
		inputs = append(inputs, httpapi.WorkerInputFile{Path: p, Size: len(b), Sha256: shaHex(b)})
	}
	j.assignInputNames(inputs)
	for _, f := range inputs {
		if err := j.downloadInput(context.Background(), f); err != nil {
			t.Fatalf("download %q: %v", f.Path, err)
		}
	}

	colonLocal := j.localPath("a:b.txt")
	underLocal := j.localPath("a_b.txt")
	if colonLocal == underLocal || strings.EqualFold(colonLocal, underLocal) {
		t.Fatalf("local names collide: %q vs %q", colonLocal, underLocal)
	}
	// The historical mapping is preserved for the first (sorted) path.
	if colonLocal != "a_b.txt" {
		t.Fatalf("a:b.txt local name = %q, want a_b.txt", colonLocal)
	}

	for p, b := range files {
		local := j.localPath(p)
		got, err := os.ReadFile(filepath.Join(dir, "input", filepath.FromSlash(local)))
		if err != nil {
			t.Fatalf("read %q -> %q: %v", p, local, err)
		}
		if string(got) != string(b) {
			t.Fatalf("%q -> %q: bytes = %q, want %q", p, local, got, b)
		}
	}
}

// Пункт 1 (второй раунд): регистр, конечные пробелы/точки, reserved names и
// сегменты папок дают разные локальные пути — сравнение без регистра.
func TestInputNamesCollisionsCaseFolders(t *testing.T) {
	paths := []string{
		"Отчёт.docx", "отчёт.docx", // регистр в имени файла
		"A/файл.txt", "a/файл.txt", // регистр в сегменте папки
		"a:b.txt", "a_b.txt", // недопустимый символ -> "_"
		"конец.", "конец ", "конец", // конечная точка/пробел
		"CON", "CON_", // reserved name -> "CON_"
	}
	j := &jobExec{names: loadLocalNames(filepath.Join(t.TempDir(), "localnames.json"))}
	j.names.assignAll(paths)

	seen := map[string]string{}
	for _, p := range paths {
		local := j.names.local(p)
		key := strings.ToLower(local)
		if prev, ok := seen[key]; ok {
			t.Fatalf("paths %q and %q both map to %q", prev, p, local)
		}
		seen[key] = p
	}
	// Every mapping is a valid single-level relative path with no leftovers.
	for _, p := range paths {
		local := j.names.local(p)
		if strings.Contains(local, ":") || strings.HasSuffix(local, ".") || strings.HasSuffix(local, " ") {
			t.Fatalf("%q -> %q is not Windows-safe", p, local)
		}
	}
}

// Пункт 1 (второй раунд): после перезапуска имена не меняются, а новый файл
// не переименовывает уже выданные имена.
func TestInputNamesStableAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	mapPath := filepath.Join(dir, "localnames.json")
	first := loadLocalNames(mapPath)
	first.assignAll([]string{"Отчёт.docx", "отчёт.docx", "a:b.txt"})

	before := map[string]string{}
	for _, p := range []string{"Отчёт.docx", "отчёт.docx", "a:b.txt"} {
		before[p] = first.local(p)
	}

	// A worker restart reloads the persisted mapping.
	second := loadLocalNames(mapPath)
	for p, want := range before {
		if got := second.local(p); got != want {
			t.Fatalf("after restart %q = %q, want %q", p, got, want)
		}
	}
	// A later-arriving file gets a fresh name and changes nothing.
	second.assignAll([]string{"Отчёт.docx", "отчёт.docx", "a:b.txt", "a_b.txt"})
	for p, want := range before {
		if got := second.local(p); got != want {
			t.Fatalf("after new file %q = %q, want %q", p, got, want)
		}
	}
	if a, b := second.local("a:b.txt"), second.local("a_b.txt"); strings.EqualFold(a, b) {
		t.Fatalf("a:b.txt and a_b.txt collide after restart: %q", a)
	}
}

// Пункт 1 (второй раунд): каждый путь, записанный в TASK.md, {{.Files}} и
// REVISION-<n>.md, существует на диске и содержит свои байты.
func TestMarkdownPathsExistWithOwnBytes(t *testing.T) {
	files := map[string][]byte{
		"a:b.txt":                          []byte("первый"),
		"a_b.txt":                          []byte("второй"),
		"revision-2/refs/доп:данные.txt":   []byte("третий"),
		"revision-2/refs/доп_данные.txt":   []byte("четвёртый"),
	}
	srv := inputServer(t, files)
	dir := t.TempDir()
	j := nameJob(t, dir, srv)
	j.asn.Revision = &httpapi.AssignmentRevision{
		Version: 2,
		Files: []httpapi.RevisionFile{
			{Path: "revision-2/refs/доп:данные.txt"},
			{Path: "revision-2/refs/доп_данные.txt"},
		},
	}
	j.inputFiles = []httpapi.WorkerInputFile{
		{Path: "a:b.txt", Size: len(files["a:b.txt"]), Sha256: shaHex(files["a:b.txt"])},
		{Path: "a_b.txt", Size: len(files["a_b.txt"]), Sha256: shaHex(files["a_b.txt"])},
		{Path: "revision-2/refs/доп:данные.txt", Revision: 2, Size: len(files["revision-2/refs/доп:данные.txt"]), Sha256: shaHex(files["revision-2/refs/доп:данные.txt"])},
		{Path: "revision-2/refs/доп_данные.txt", Revision: 2, Size: len(files["revision-2/refs/доп_данные.txt"]), Sha256: shaHex(files["revision-2/refs/доп_данные.txt"])},
	}
	j.assignInputNames(j.inputFiles)
	for _, f := range j.inputFiles {
		if err := j.downloadInput(context.Background(), f); err != nil {
			t.Fatalf("download %q: %v", f.Path, err)
		}
	}
	if err := j.writeTaskMD(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "TASK.md"))
	if err != nil {
		t.Fatal(err)
	}
	md := string(raw)
	for _, ref := range []string{j.localPath("a:b.txt"), j.localPath("a_b.txt")} {
		if !strings.Contains(md, "input/"+ref) {
			t.Fatalf("TASK.md does not reference %q:\n%s", ref, md)
		}
	}
	// The {{.Files}} prompt block references the initial files by the same names.
	filesBlock := j.filesLine()
	for _, ref := range []string{j.localPath("a:b.txt"), j.localPath("a_b.txt")} {
		if !strings.Contains(filesBlock, "input/"+ref) {
			t.Fatalf("Files block does not reference %q:\n%s", ref, filesBlock)
		}
	}
	// REVISION-2.md references the revision files by the same names.
	revMD := j.revisionMD(j.asn.Revision, nil)
	for _, ref := range []string{j.localPath("revision-2/refs/доп:данные.txt"), j.localPath("revision-2/refs/доп_данные.txt")} {
		if !strings.Contains(revMD, "input/"+ref) {
			t.Fatalf("REVISION-2.md does not reference %q:\n%s", ref, revMD)
		}
	}

	// Every referenced Markdown path resolves to a file with the right bytes.
	for _, mdText := range []string{md, filesBlock, revMD} {
		for _, line := range strings.Split(mdText, "\n") {
			line = strings.TrimSpace(line)
			var rel string
			switch {
			case strings.HasPrefix(line, "- input/"):
				rel = strings.TrimPrefix(line, "- input/")
			case strings.Contains(line, "Вырезка: input/"):
				rel = line[strings.Index(line, "Вырезка: input/")+len("Вырезка: "):]
			default:
				continue
			}
			if i := strings.IndexByte(rel, ' '); i >= 0 {
				rel = rel[:i]
			}
			rel = strings.TrimPrefix(rel, "input/")
			ref := filepath.Join(dir, "input", filepath.FromSlash(rel))
			got, err := os.ReadFile(ref)
			if err != nil {
				t.Fatalf("Markdown reference %q does not exist: %v", rel, err)
			}
			var want []byte
			for p, b := range files {
				if filepath.ToSlash(j.localPath(p)) == filepath.ToSlash(rel) {
					want = b
				}
			}
			if want == nil {
				t.Fatalf("Markdown reference %q does not match any server file", rel)
			}
			if string(got) != string(want) {
				t.Fatalf("%q contains %q, want %q", rel, got, want)
			}
		}
	}
}
