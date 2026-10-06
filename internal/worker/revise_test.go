package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/luckydiss/studlance_ai/internal/config"
	"github.com/luckydiss/studlance_ai/internal/httpapi"
	"github.com/luckydiss/studlance_ai/internal/worker/client"
)

// revisionHarness is a jobExec with a v1 snapshot holding one page image and
// one remark, plus a fake server collecting remark-crop uploads.
type revisionHarness struct {
	j       *jobExec
	dir     string
	mdPath  string
	crops   string
	uploads *sync.Map // path -> []byte
	got     *atomic.Int32
}

func newRevisionHarness(t *testing.T) *revisionHarness {
	t.Helper()
	dir := t.TempDir()
	snap := filepath.Join(dir, ".studlance", "snapshots", "v1")
	pages := filepath.Join(snap, "pages", "0")
	if err := os.MkdirAll(pages, 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	page, err := os.Create(filepath.Join(pages, "1.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(page, img); err != nil {
		t.Fatal(err)
	}
	_ = page.Close()

	info := snapshotInfo{Documents: []snapshotInfoDoc{{Idx: 0, FilePath: "out/чертёж.cdw", Title: "Чертёж", PageCount: 1}}}
	raw, _ := json.Marshal(info)
	if err := os.WriteFile(filepath.Join(snap, "info.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	uploads := &sync.Map{}
	got := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "/input/revision/") && strings.Contains(req.URL.Path, "/remarks/") {
			b, _ := io.ReadAll(req.Body)
			uploads.Store(req.URL.Path, b)
			got.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rev := &httpapi.AssignmentRevision{
		Version: 2,
		Comment: "Поправьте, пожалуйста",
		Remarks: []httpapi.AssignmentRemark{{
			Idx: 1, Text: "Уточните расчёт прогиба", DocumentTitle: "Чертёж",
			FilePath: "out/чертёж.cdw", Page: 1,
			X: 0.1, Y: 0.1, W: 0.2, H: 0.2,
		}},
	}
	j := &jobExec{
		w: &Worker{
			cfg:    config.Worker{},
			cl:     client.New(srv.URL, "tok"),
			logger: logger,
		},
		asn:   &httpapi.Assignment{JobId: "j1", Epoch: 1, Version: 2, Revision: rev},
		dir:   dir,
		log:   logger,
		names: loadLocalNames(filepath.Join(dir, ".studlance", "localnames.json")),
	}
	return &revisionHarness{
		j:       j,
		dir:     dir,
		mdPath:  filepath.Join(dir, "REVISION-2.md"),
		crops:   revisionCropsDir(dir, 2),
		uploads: uploads,
		got:     got,
	}
}

func (h *revisionHarness) cropPath() string { return filepath.Join(h.crops, "1.png") }

func (h *revisionHarness) readCrop(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(h.cropPath())
	if err != nil {
		t.Fatalf("crop: %v", err)
	}
	if !validCrop(h.cropPath()) {
		t.Fatal("crop is not a valid PNG")
	}
	return b
}

func (h *revisionHarness) readMD(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(h.mdPath)
	if err != nil {
		t.Fatalf("REVISION-2.md: %v", err)
	}
	return string(b)
}

// Пункт 7 (второй раунд): повтор prepareRevision с удалённым PNG
// восстанавливает вырезку из предыдущего снимка и оставляет согласованную
// ссылку; идемпотентная повторная загрузка тех же байтов.
func TestPrepareRevisionRestoresMissingCrop(t *testing.T) {
	h := newRevisionHarness(t)
	ctx := context.Background()

	if err := h.j.prepareRevision(ctx); err != nil {
		t.Fatalf("prepareRevision: %v", err)
	}
	first := h.readCrop(t)
	md := h.readMD(t)
	if !strings.Contains(md, "Вырезка: revision-crops/2/1.png") {
		t.Fatalf("REVISION-2.md has no crop link:\n%s", md)
	}
	if h.got.Load() == 0 {
		t.Fatal("the crop was never uploaded")
	}

	// The reproduction: the crop disappears, the Markdown stays.
	if err := os.Remove(h.cropPath()); err != nil {
		t.Fatal(err)
	}
	if err := h.j.prepareRevision(ctx); err != nil {
		t.Fatalf("second prepareRevision: %v", err)
	}
	second := h.readCrop(t)
	if string(first) != string(second) {
		t.Fatal("restored crop differs from the original one")
	}
	if md2 := h.readMD(t); !strings.Contains(md2, "Вырезка: revision-crops/2/1.png") {
		t.Fatalf("REVISION-2.md lost the crop link after the restore:\n%s", md2)
	}
	if h.got.Load() < 2 {
		t.Fatalf("uploads = %d, want the crop uploaded again (idempotently)", h.got.Load())
	}
}

// Пункт 7 (второй раунд): прерывание подготовки — битая/частичная вырезка и
// оставшийся Markdown. Вырезка восстанавливается, а если её неоткуда взять,
// ссылка не пишется и файл удаляется.
func TestPrepareRevisionRebuildsInterruptedPreparation(t *testing.T) {
	h := newRevisionHarness(t)
	ctx := context.Background()

	// A stale REVISION-2.md with a link and a truncated PNG on disk: exactly
	// what an interrupted preparation leaves behind.
	stale := "# Доработка — версия 2\n\n1. «x» — y (out/чертёж.cdw), стр. 1. Вырезка: revision-crops/2/1.png\n"
	if err := os.MkdirAll(h.crops, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.mdPath, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	truncated := append([]byte("\x89PNG\r\n\x1a\n"), []byte("not really a png")...)
	if err := os.WriteFile(h.cropPath(), truncated, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := h.j.prepareRevision(ctx); err != nil {
		t.Fatalf("prepareRevision: %v", err)
	}
	h.readCrop(t) // must be a real PNG again
	if md := h.readMD(t); !strings.Contains(md, "Вырезка: revision-crops/2/1.png") {
		t.Fatalf("rebuilt crop is not referenced:\n%s", md)
	}

	// Now the crop cannot be rebuilt (the snapshot page is gone): the link
	// must disappear together with the file.
	if err := os.RemoveAll(filepath.Join(h.dir, ".studlance", "snapshots", "v1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(h.cropPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.mdPath, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.j.prepareRevision(ctx); err != nil {
		t.Fatalf("prepareRevision without a snapshot: %v", err)
	}
	if md := h.readMD(t); strings.Contains(md, "Вырезка:") {
		t.Fatalf("REVISION-2.md references a crop that cannot exist:\n%s", md)
	}
	if _, err := os.Stat(h.cropPath()); !os.IsNotExist(err) {
		t.Fatal("a stale crop file survived")
	}
	// The remark itself still reaches the agent.
	if md := h.readMD(t); !strings.Contains(md, "Уточните расчёт прогиба") {
		t.Fatalf("REVISION-2.md lost the remark text:\n%s", md)
	}
}

// Пункт 2 (третий раунд): вырезки отделены от входных файлов. Вложение
// клиента с именем remarks/1.png остаётся в input и не перезаписывается
// вырезкой; вырезка лежит в revision-crops/2/1.png.
func TestPrepareRevisionKeepsInputAttachment(t *testing.T) {
	plain := []byte("клиентское вложение, не вырезка")
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 10, B: 10, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	validPNG := pngBuf.Bytes()

	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"arbitrary bytes", plain},
		{"valid different png", validPNG},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newRevisionHarness(t)
			ctx := context.Background()
			h.j.asn.Revision.Files = []httpapi.RevisionFile{{Path: "revision-2/remarks/1.png"}}
			inputPath := filepath.Join(h.dir, "input", "revision-2", "remarks", "1.png")
			if err := os.MkdirAll(filepath.Dir(inputPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(inputPath, tc.data, 0o644); err != nil {
				t.Fatal(err)
			}

			for run := 1; run <= 3; run++ {
				if err := h.j.prepareRevision(ctx); err != nil {
					t.Fatalf("prepareRevision #%d: %v", run, err)
				}
				got, err := os.ReadFile(inputPath)
				if err != nil {
					t.Fatalf("input attachment disappeared on run %d: %v", run, err)
				}
				if !bytes.Equal(got, tc.data) {
					t.Fatalf("input attachment changed on run %d: %q", run, got)
				}
				crop := h.readCrop(t)
				if bytes.Equal(crop, tc.data) {
					t.Fatal("the crop is the input attachment: namespaces are not separated")
				}
				md := h.readMD(t)
				if !strings.Contains(md, "Вырезка: revision-crops/2/1.png") {
					t.Fatalf("run %d: REVISION-2.md has no crop link:\n%s", run, md)
				}
				if !strings.Contains(md, "- input/revision-2/remarks/1.png") {
					t.Fatalf("run %d: REVISION-2.md lost the attachment reference:\n%s", run, md)
				}
				if strings.Contains(md, "Вырезка: input/") {
					t.Fatalf("run %d: the crop link points into input/:\n%s", run, md)
				}
			}

			// Deleting the crop restores it without touching the attachment.
			if err := os.Remove(h.cropPath()); err != nil {
				t.Fatal(err)
			}
			if err := h.j.prepareRevision(ctx); err != nil {
				t.Fatal(err)
			}
			h.readCrop(t)
			got, err := os.ReadFile(inputPath)
			if err != nil {
				t.Fatalf("input attachment disappeared after restore: %v", err)
			}
			if !bytes.Equal(got, tc.data) {
				t.Fatalf("input attachment changed after restore: %q", got)
			}
		})
	}
}

// Пункт 2 (третий раунд): начальный вход input/revision-2/remarks/1.png не
// считается готовой вырезкой — вырезка строится отдельно, вход не трогается.
func TestPrepareRevisionInitialInputNotACrop(t *testing.T) {
	h := newRevisionHarness(t)
	ctx := context.Background()
	initial := filepath.Join(h.dir, "input", "revision-2", "remarks", "1.png")
	if err := os.MkdirAll(filepath.Dir(initial), 0o755); err != nil {
		t.Fatal(err)
	}
	// Invalid PNG on purpose: a stale input must not be treated as a ready
	// crop, so the crop is rebuilt next to it.
	if err := os.WriteFile(initial, []byte("not a png at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.j.prepareRevision(ctx); err != nil {
		t.Fatal(err)
	}
	crop := h.readCrop(t)
	if strings.Contains(string(crop), "not a png") {
		t.Fatal("the crop was taken from the input file")
	}
	if got, err := os.ReadFile(initial); err != nil || string(got) != "not a png at all" {
		t.Fatalf("initial input changed: %q %v", got, err)
	}
}

// Пункт 7 (второй раунд): continue/answer повторяет подготовку идемпотентно:
// те же имена, те же байты, атомарная запись без временных файлов.
func TestPrepareRevisionIdempotentOnContinue(t *testing.T) {
	h := newRevisionHarness(t)
	ctx := context.Background()

	if err := h.j.prepareRevision(ctx); err != nil {
		t.Fatal(err)
	}
	md1 := h.readMD(t)
	crop1 := h.readCrop(t)

	// A continue/answer runs the preparation again.
	if err := h.j.prepareRevision(ctx); err != nil {
		t.Fatal(err)
	}
	if md2 := h.readMD(t); md1 != md2 {
		t.Fatalf("REVISION-2.md changed between continue runs:\n%s\n---\n%s", md1, md2)
	}
	if crop2 := h.readCrop(t); string(crop1) != string(crop2) {
		t.Fatal("crop bytes changed between continue runs")
	}
	if n := strings.Count(md1, "Вырезка:"); n != 1 {
		t.Fatalf("crop links = %d, want 1", n)
	}
	entries, err := os.ReadDir(h.crops)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".crop-") || strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}
