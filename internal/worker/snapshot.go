package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/luckydiss/studlance_ai/internal/httpapi"
	"github.com/luckydiss/studlance_ai/internal/worker/preview"
)

// manifestDoc is one entry of the agent-written manifest.json.
type manifestDoc struct {
	File    string `json:"file"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Preview string `json:"preview"`
}

type manifest struct {
	Title     string        `json:"title"`
	Documents []manifestDoc `json:"documents"`
}

// snapshotInfo is stored in .studlance/snapshots/<name>/info.json and maps
// documents to rendered pages for later diffs and revision crops.
type snapshotInfo struct {
	Documents []snapshotInfoDoc `json:"documents"`
}

type snapshotInfoDoc struct {
	Idx       int    `json:"idx"`
	FilePath  string `json:"file_path"`
	Title     string `json:"title"`
	PageCount int    `json:"page_count"`
}

// readManifest reads and validates manifest.json (05-worker.md «Снимок версии»).
func (j *jobExec) readManifest() (manifest, error) {
	raw, err := os.ReadFile(filepath.Join(j.dir, "manifest.json"))
	if err != nil {
		return manifest{}, errors.New("нет manifest.json")
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return manifest{}, fmt.Errorf("manifest.json: %v", err)
	}
	if len(m.Documents) == 0 {
		return manifest{}, errors.New("manifest.json: пустой список документов")
	}
	for _, d := range m.Documents {
		if !strings.HasPrefix(d.File, "out/") {
			return manifest{}, fmt.Errorf("manifest.json: файл %q вне out/", d.File)
		}
		if _, err := os.Stat(filepath.Join(j.dir, filepath.FromSlash(d.File))); err != nil {
			return manifest{}, fmt.Errorf("manifest.json: файл %s не найден", d.File)
		}
		if d.Preview != "" && !strings.HasPrefix(d.Preview, "preview/") {
			return manifest{}, fmt.Errorf("manifest.json: превью %q вне preview/", d.Preview)
		}
	}
	return m, nil
}

// commitDraft snapshots and commits the draft, advancing the stage to verify.
func (j *jobExec) commitDraft(ctx context.Context) bool {
	title := summaryTitle(filepath.Join(j.dir, "SUMMARY.md"))
	if err := j.snapshot(ctx, "draft", "", title, nil); err != nil {
		return j.commitFailed(ctx, err)
	}
	return true
}

// commitVersion snapshots and commits version n, diffed against prevSnap
// ("draft" for v1, "v<n-1>" otherwise), with verification.json attached.
func (j *jobExec) commitVersion(ctx context.Context, n int, prevSnap string) bool {
	var ver *httpapi.Verification
	if raw, err := os.ReadFile(filepath.Join(j.dir, "verification.json")); err == nil {
		var v httpapi.Verification
		if json.Unmarshal(raw, &v) == nil {
			ver = &v
		}
	}
	if err := j.snapshot(ctx, fmt.Sprintf("v%d", n), prevSnap, "", ver); err != nil {
		return j.commitFailed(ctx, err)
	}
	return true
}

// commitFailed maps a snapshot/commit error to a stage failure or an abort.
func (j *jobExec) commitFailed(ctx context.Context, err error) bool {
	if isStale(err) || isConflict(err) {
		j.log.Error("commit lost lease", "err", err)
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	j.log.Error("snapshot failed", "err", err)
	j.finish(ctx, httpapi.FinishRequestOutcomeFailed, nil, "не удалось зафиксировать результат: "+err.Error())
	return false
}

// snapshot builds, uploads and commits one snapshot (05-worker.md «Снимок версии»).
func (j *jobExec) snapshot(ctx context.Context, name, prevName, title string, ver *httpapi.Verification) error {
	m, err := j.readManifest()
	if err != nil {
		return err
	}

	snapDir := filepath.Join(j.snapshotsDir(), name)
	if err := os.RemoveAll(snapDir); err != nil {
		return err
	}
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return err
	}
	// Local copies of out/ and preview/ (for diffs and remark crops).
	if err := copyTree(filepath.Join(j.dir, "out"), filepath.Join(snapDir, "out")); err != nil {
		return fmt.Errorf("копия out/: %w", err)
	}
	if _, err := os.Stat(filepath.Join(j.dir, "preview")); err == nil {
		if err := copyTree(filepath.Join(j.dir, "preview"), filepath.Join(snapDir, "preview")); err != nil {
			return fmt.Errorf("копия preview/: %w", err)
		}
	}

	// Upload every out/ file, then every preview/ file (the server validates
	// preview_path blobs at commit).
	outRoot := filepath.Join(snapDir, "out")
	err = filepath.WalkDir(outRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(outRoot, p)
		if err != nil {
			return err
		}
		return j.uploadFile(ctx, name, filepath.ToSlash(rel), p)
	})
	if err != nil {
		return err
	}
	prevRoot := filepath.Join(snapDir, "preview")
	if _, err := os.Stat(prevRoot); err == nil {
		err = filepath.WalkDir(prevRoot, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(prevRoot, p)
			if err != nil {
				return err
			}
			return j.uploadFile(ctx, name, "preview/"+filepath.ToSlash(rel), p)
		})
		if err != nil {
			return err
		}
	}

	prevInfo := loadSnapshotInfo(filepath.Join(j.snapshotsDir(), prevName))

	info := snapshotInfo{}
	docs := make([]httpapi.SnapshotDocument, 0, len(m.Documents))
	for idx, d := range m.Documents {
		doc, idoc, err := j.snapshotDoc(ctx, snapDir, name, prevName, prevInfo, idx, d)
		if err != nil {
			return err
		}
		docs = append(docs, doc)
		info.Documents = append(info.Documents, idoc)
	}

	raw, err := json.MarshalIndent(info, "", "  ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(snapDir, "info.json"), raw, 0o644)
	}

	req := httpapi.CommitSnapshotRequest{Epoch: j.asn.Epoch, Documents: docs, Verification: ver}
	if title != "" {
		req.Title = &title
	}
	return j.w.cl.CommitSnapshot(ctx, j.asn.JobId, name, req)
}

// snapshotDoc renders and uploads one document's preview, pages and thumbs,
// and builds its commit entry.
func (j *jobExec) snapshotDoc(ctx context.Context, snapDir, snapName, prevName string, prevInfo snapshotInfo, idx int, d manifestDoc) (httpapi.SnapshotDocument, snapshotInfoDoc, error) {
	rel := strings.TrimPrefix(d.File, "out/")
	srcPath := filepath.Join(snapDir, "out", filepath.FromSlash(rel))

	doc := httpapi.SnapshotDocument{
		Idx:      idx,
		Title:    d.Title,
		Kind:     d.Kind,
		FilePath: rel,
	}
	idoc := snapshotInfoDoc{Idx: idx, FilePath: rel, Title: d.Title}

	// Pick or produce the preview PDF.
	pdfPath, previewRel := j.docPreviewPDF(ctx, snapDir, srcPath, rel, d)
	switch {
	case pdfPath != "":
		if previewRel != "" {
			doc.PreviewPath = &previewRel
		}
	case isImageFile(rel):
		// An image is its own single page, no PDF.
		pngPath := filepath.Join(snapDir, "pages", itoa(idx), "1.png")
		if err := imageToPNG(srcPath, pngPath); err != nil {
			j.log.Error("image page", "file", rel, "err", err)
			return doc, idoc, nil
		}
		pages, err := j.uploadPages(ctx, snapDir, snapName, prevName, prevInfo, idx, rel, []string{pngPath})
		if err != nil {
			return doc, idoc, err
		}
		doc.Pages = pages
		doc.PageCount = len(pages)
		idoc.PageCount = len(pages)
		return doc, idoc, nil
	default:
		// No preview possible (.cdw, source code, failed conversion).
		return doc, idoc, nil
	}

	pagesDir := filepath.Join(snapDir, "pages", itoa(idx))
	pagePaths, err := j.w.rend.RenderPDF(ctx, pdfPath, pagesDir)
	if err != nil {
		// A broken preview must not fail the stage: document without pages.
		j.log.Error("pdftoppm failed", "file", rel, "err", err)
		return doc, idoc, nil
	}
	// Normalize p-N.png (pdftoppm) to N.png — later diffs and revision crops
	// address pages by number.
	for i, p := range pagePaths {
		norm := filepath.Join(pagesDir, fmt.Sprintf("%d.png", i+1))
		if p != norm {
			if err := os.Rename(p, norm); err != nil {
				return doc, idoc, fmt.Errorf("страница %d: %w", i+1, err)
			}
			pagePaths[i] = norm
		}
	}
	pages, err := j.uploadPages(ctx, snapDir, snapName, prevName, prevInfo, idx, rel, pagePaths)
	if err != nil {
		return doc, idoc, err
	}
	doc.Pages = pages
	doc.PageCount = len(pages)
	idoc.PageCount = len(pages)
	return doc, idoc, nil
}

// docPreviewPDF returns the PDF to render for a document and the preview_path
// to commit ("" when the document itself is the PDF or no preview exists).
func (j *jobExec) docPreviewPDF(ctx context.Context, snapDir, srcPath, rel string, d manifestDoc) (pdfPath, previewRel string) {
	if d.Preview != "" {
		p := filepath.Join(snapDir, filepath.FromSlash(d.Preview))
		if _, err := os.Stat(p); err == nil {
			return p, d.Preview
		}
	}
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".pdf":
		return srcPath, ""
	case ".png", ".jpg", ".jpeg":
		return "", ""
	case ".docx", ".doc", ".rtf", ".odt", ".xlsx", ".xls", ".pptx":
		base := strings.TrimSuffix(filepath.Base(rel), ext)
		dstRel := "preview/" + base + ".pdf"
		dst := filepath.Join(snapDir, filepath.FromSlash(dstRel))
		ok, err := preview.ConvertToPDF(ctx, srcPath, dst)
		if err != nil {
			j.log.Error("convert to pdf", "file", rel, "err", err)
		}
		if ok {
			if err := j.uploadFile(ctx, filepath.Base(snapDir), dstRel, dst); err != nil {
				j.log.Error("upload converted preview", "err", err)
				return "", ""
			}
			return dst, dstRel
		}
	}
	return "", ""
}

// uploadPages writes thumbs, uploads pages+thumbs and diffs against the
// previous snapshot (matching the document by file_path).
func (j *jobExec) uploadPages(ctx context.Context, snapDir, snapName, prevName string, prevInfo snapshotInfo, idx int, rel string, pagePaths []string) ([]httpapi.SnapshotPage, error) {
	pages := make([]httpapi.SnapshotPage, 0, len(pagePaths))
	prevIdx := -1
	if prevName != "" {
		prevIdx = findPrevDoc(prevInfo, rel)
	}
	for i, pp := range pagePaths {
		pageNo := i + 1
		w, h, err := preview.ImageSize(pp)
		if err != nil {
			return nil, fmt.Errorf("страница %d: %w", pageNo, err)
		}
		thumbPath := filepath.Join(snapDir, "thumbs", itoa(idx), fmt.Sprintf("%d.png", pageNo))
		if err := preview.WriteThumb(pp, thumbPath); err != nil {
			return nil, fmt.Errorf("мини-копия %d: %w", pageNo, err)
		}
		if err := j.uploadReader(ctx, snapName, "page", idx, pageNo, pp); err != nil {
			return nil, err
		}
		if err := j.uploadReader(ctx, snapName, "thumb", idx, pageNo, thumbPath); err != nil {
			return nil, err
		}
		sp := httpapi.SnapshotPage{Page: pageNo, Width: w, Height: h}
		if prevName != "" {
			boxes := diffWithPrev(prevName, j.snapshotsDir(), prevIdx, idx, pageNo, pp)
			if len(boxes) > 0 {
				api := make([]httpapi.ChangedBox, 0, len(boxes))
				for _, b := range boxes {
					api = append(api, httpapi.ChangedBox{X: float32(b.X), Y: float32(b.Y), W: float32(b.W), H: float32(b.H)})
				}
				sp.ChangedBoxes = &api
			}
		}
		pages = append(pages, sp)
	}
	return pages, nil
}

// findPrevDoc returns the idx of filePath in the previous snapshot info.
func findPrevDoc(info snapshotInfo, filePath string) int {
	for _, d := range info.Documents {
		if d.FilePath == filePath {
			return d.Idx
		}
	}
	return -1
}

// diffWithPrev compares one page against the previous snapshot's page.
func diffWithPrev(prevName, snapshotsDir string, prevIdx, idx, pageNo int, curPath string) []preview.Box {
	if prevIdx < 0 {
		return []preview.Box{{X: 0, Y: 0, W: 1, H: 1}}
	}
	prevPath := filepath.Join(snapshotsDir, prevName, "pages", itoa(prevIdx), fmt.Sprintf("%d.png", pageNo))
	boxes, err := preview.DiffPages(prevPath, curPath)
	if err != nil {
		return []preview.Box{{X: 0, Y: 0, W: 1, H: 1}}
	}
	return boxes
}

// ---------- small helpers ----------

func (j *jobExec) uploadFile(ctx context.Context, snap, rel, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return j.w.cl.PutSnapshotFile(ctx, j.asn.JobId, snap, j.asn.Epoch, rel, f)
}

func (j *jobExec) uploadReader(ctx context.Context, snap, kind string, idx, page int, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if kind == "page" {
		return j.w.cl.PutSnapshotPage(ctx, j.asn.JobId, snap, j.asn.Epoch, idx, page, f)
	}
	return j.w.cl.PutSnapshotThumb(ctx, j.asn.JobId, snap, j.asn.Epoch, idx, page, f)
}

func loadSnapshotInfo(dir string) snapshotInfo {
	var info snapshotInfo
	raw, err := os.ReadFile(filepath.Join(dir, "info.json"))
	if err != nil {
		return info
	}
	_ = json.Unmarshal(raw, &info)
	return info
}

// summaryTitle reads the first line of SUMMARY.md (without «#», ≤ 120 chars).
func summaryTitle(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(raw), "\n")
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
	r := []rune(line)
	if len(r) > 120 {
		line = string(r[:120])
	}
	return line
}

func isImageFile(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".png", ".jpg", ".jpeg":
		return true
	}
	return false
}

// imageToPNG converts any decodable image to PNG.
func imageToPNG(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	img, _, err := image.Decode(f)
	_ = f.Close()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	return png.Encode(out, img)
}

func copyTree(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		return copyOne(p, to)
	})
}

func copyOne(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
