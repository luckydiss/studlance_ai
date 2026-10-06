package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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

// assignAndDownload registers the server files in the given order, downloads
// them and returns the job and its working folder.
func assignAndDownload(t *testing.T, order []string, files map[string][]byte) (*jobExec, string) {
	t.Helper()
	srv := inputServer(t, files)
	dir := t.TempDir()
	j := nameJob(t, dir, srv)
	inputs := make([]httpapi.WorkerInputFile, 0, len(order))
	for _, p := range order {
		inputs = append(inputs, httpapi.WorkerInputFile{Path: p, Size: len(files[p]), Sha256: shaHex(files[p])})
	}
	j.inputFiles = inputs
	j.assignInputNames(inputs)
	for _, f := range inputs {
		if err := j.downloadInput(context.Background(), f); err != nil {
			t.Fatalf("download %q: %v", f.Path, err)
		}
	}
	return j, dir
}

// checkNameTree fails when two local paths collide case-insensitively or when
// one local file is a parent directory of another path.
func checkNameTree(t *testing.T, locals []string) {
	t.Helper()
	seen := map[string]string{}
	for _, l := range locals {
		key := strings.ToLower(l)
		if prev, ok := seen[key]; ok {
			t.Fatalf("local paths %q and %q collide (case-insensitive)", prev, l)
		}
		seen[key] = l
	}
	for _, l := range locals {
		segs := strings.Split(l, "/")
		for i := 1; i < len(segs); i++ {
			prefix := strings.ToLower(strings.Join(segs[:i], "/"))
			if parent, ok := seen[prefix]; ok {
				t.Fatalf("local file %q is a parent directory of %q", parent, l)
			}
		}
	}
}

// checkBytes fails unless every server path is a regular file with its bytes.
func checkBytes(t *testing.T, j *jobExec, dir string, files map[string][]byte) {
	t.Helper()
	for p, want := range files {
		local := j.localPath(p)
		path := filepath.Join(dir, "input", filepath.FromSlash(local))
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%q -> %q: %v", p, local, err)
		}
		if fi.IsDir() {
			t.Fatalf("%q -> %q is a directory, want a file", p, local)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %q -> %q: %v", p, local, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%q -> %q: bytes = %q, want %q", p, local, got, want)
		}
	}
}

func reversed(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[len(in)-1-i] = s
	}
	return out
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
	j, dir := assignAndDownload(t, []string{"a:b.txt", "a_b.txt"}, files)

	colonLocal := j.localPath("a:b.txt")
	underLocal := j.localPath("a_b.txt")
	if strings.EqualFold(colonLocal, underLocal) {
		t.Fatalf("local names collide: %q vs %q", colonLocal, underLocal)
	}
	// The historical mapping is preserved for the first (sorted) path.
	if colonLocal != "a_b.txt" {
		t.Fatalf("a:b.txt local name = %q, want a_b.txt", colonLocal)
	}
	checkNameTree(t, []string{colonLocal, underLocal})
	checkBytes(t, j, dir, files)
}

// Пункт 1 (третий раунд): отображение учитывает дерево сегментов, а не
// только равенство полного пути. Локальный файл не может быть папкой или
// предком другого файла; порядок входного списка не влияет на результат.
func TestInputNamesFileDirCollisions(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
	}{
		{"colon file vs mapped dir", []string{"a:b", "a_b/child.txt"}},
		{"mapped file vs colon dir", []string{"a_b", "a:b/child.txt"}},
		{"intermediate segment", []string{"p/q:r/a.txt", "p/q_r"}},
		{"deep path vs mapped file", []string{"a_b/c/d.txt", "a:b"}},
		{"case variants", []string{"От/файл.txt", "от/файл.txt", "Dir/x.txt", "dir/x.txt"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{}
			for i, p := range tc.paths {
				files[p] = []byte(fmt.Sprintf("bytes-%d-%s", i, p))
			}
			var first []string
			for oi, order := range [][]string{tc.paths, reversed(tc.paths)} {
				j, dir := assignAndDownload(t, order, files)
				locals := make([]string, 0, len(tc.paths))
				for _, p := range tc.paths {
					locals = append(locals, j.localPath(p))
				}
				checkNameTree(t, locals)
				checkBytes(t, j, dir, files)
				if oi == 0 {
					first = locals
				} else if !slices.Equal(first, locals) {
					t.Fatalf("input order changed the mapping:\n%v\n%v", first, locals)
				}
			}
		})
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

	locals := make([]string, 0, len(paths))
	for _, p := range paths {
		local := j.names.local(p)
		locals = append(locals, local)
		if strings.Contains(local, ":") || strings.HasSuffix(local, ".") || strings.HasSuffix(local, " ") {
			t.Fatalf("%q -> %q is not Windows-safe", p, local)
		}
	}
	checkNameTree(t, locals)
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

// Пункт 1 (третий раунд): сохранённая карта перезагружается, старые
// корректные пути остаются рабочими, а добавленный конфликтующий вход
// получает безопасное имя.
func TestInputNamesReloadThenConflictingInput(t *testing.T) {
	dir := t.TempDir()
	mapPath := filepath.Join(dir, "localnames.json")
	first := loadLocalNames(mapPath)
	first.assignAll([]string{"a:b", "a_b/child.txt"})
	before := map[string]string{}
	for _, p := range []string{"a:b", "a_b/child.txt"} {
		before[p] = first.local(p)
	}
	checkNameTree(t, []string{before["a:b"], before["a_b/child.txt"]})

	second := loadLocalNames(mapPath)
	for p, want := range before {
		if got := second.local(p); got != want {
			t.Fatalf("after reload %q = %q, want %q", p, got, want)
		}
	}
	// The new input collides with the directory the first file created.
	second.assignAll([]string{"a:b", "a_b/child.txt", "a_b"})
	for p, want := range before {
		if got := second.local(p); got != want {
			t.Fatalf("after conflicting input %q = %q, want %q", p, got, want)
		}
	}
	locals := []string{second.local("a:b"), second.local("a_b/child.txt"), second.local("a_b")}
	checkNameTree(t, locals)
}

// Пункт 1 (третий раунд): карта, испорченная прежним воркером (файл и папка
// по одному пути), при загрузке чинится: корректный путь сохраняет имя, а
// конфликтующий получает безопасное.
func TestInputNamesRepairsBrokenSavedMap(t *testing.T) {
	dir := t.TempDir()
	mapPath := filepath.Join(dir, "localnames.json")
	broken := `{"a:b": "a_b", "a_b/child.txt": "a_b/child.txt"}`
	if err := os.WriteFile(mapPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	m := loadLocalNames(mapPath)
	colon := m.local("a:b")
	child := m.local("a_b/child.txt")
	if colon != "a_b" {
		t.Fatalf("correct path renamed: %q", colon)
	}
	checkNameTree(t, []string{colon, child})
	if !strings.HasPrefix(child, "a_b~") {
		t.Fatalf("conflicting path got %q, want a ~N variant of a_b", child)
	}
	// The repair is persisted.
	again := loadLocalNames(mapPath)
	if got := again.local("a_b/child.txt"); got != child {
		t.Fatalf("repair not persisted: %q != %q", got, child)
	}
}

// Пункт 1 (второй/третий раунд): каждый путь, записанный в TASK.md,
// {{.Files}} и REVISION-<n>.md, существует на диске и содержит свои байты —
// включая случай, когда один вход требует папку там, где уже лежит файл.
func TestMarkdownPathsExistWithOwnBytes(t *testing.T) {
	files := map[string][]byte{
		"a:b.txt":           []byte("первый"),
		"a_b.txt":           []byte("второй"),
		"a_b.txt/child.txt": []byte("третий"),
		"revision-2/refs/доп:данные.txt": []byte("четвёртый"),
		"revision-2/refs/доп_данные.txt": []byte("пятый"),
	}
	srv := inputServer(t, files)
	dir := t.TempDir()
	j := nameJob(t, dir, srv)
	initial := []string{"a:b.txt", "a_b.txt", "a_b.txt/child.txt"}
	revFiles := []string{"revision-2/refs/доп:данные.txt", "revision-2/refs/доп_данные.txt"}
	j.asn.Revision = &httpapi.AssignmentRevision{
		Version: 2,
		Files: []httpapi.RevisionFile{
			{Path: revFiles[0]},
			{Path: revFiles[1]},
		},
	}
	for _, p := range initial {
		j.inputFiles = append(j.inputFiles, httpapi.WorkerInputFile{Path: p, Size: len(files[p]), Sha256: shaHex(files[p])})
	}
	for _, p := range revFiles {
		j.inputFiles = append(j.inputFiles, httpapi.WorkerInputFile{Path: p, Revision: 2, Size: len(files[p]), Sha256: shaHex(files[p])})
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
	filesBlock := j.filesLine()
	revMD := j.revisionMD(j.asn.Revision, nil)

	locals := make([]string, 0, len(files))
	for p := range files {
		locals = append(locals, j.localPath(p))
	}
	checkNameTree(t, locals)
	checkBytes(t, j, dir, files)

	// Every Markdown path resolves to its own file with its own bytes.
	for _, mdText := range []string{md, filesBlock, revMD} {
		refs := 0
		for _, line := range strings.Split(mdText, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "- input/") {
				continue
			}
			rel := strings.TrimPrefix(line, "- input/")
			if i := strings.IndexByte(rel, ' '); i >= 0 {
				rel = rel[:i]
			}
			refs++
			var want []byte
			for p, b := range files {
				if filepath.ToSlash(j.localPath(p)) == filepath.ToSlash(rel) {
					want = b
				}
			}
			if want == nil {
				t.Fatalf("Markdown reference %q does not match any server file", rel)
			}
			got, err := os.ReadFile(filepath.Join(dir, "input", filepath.FromSlash(rel)))
			if err != nil {
				t.Fatalf("Markdown reference %q does not exist: %v", rel, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%q contains %q, want %q", rel, got, want)
			}
		}
		if refs == 0 {
			t.Fatalf("no file references found in:\n%s", mdText)
		}
	}
	// TASK.md and the prompt list the same three initial files, REVISION the two
	// revision ones.
	if n := strings.Count(md, "- input/"); n != len(initial) {
		t.Fatalf("TASK.md references = %d, want %d", n, len(initial))
	}
	if n := strings.Count(filesBlock, "- input/"); n != len(initial) {
		t.Fatalf("Files block references = %d, want %d", n, len(initial))
	}
	if n := strings.Count(revMD, "- input/"); n != len(revFiles) {
		t.Fatalf("REVISION-2.md references = %d, want %d", n, len(revFiles))
	}
}
