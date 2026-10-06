package worker

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/luckydiss/studlance_ai/internal/httpapi"
)

// prepareRevision builds the rework package before a revise stage
// (05-worker.md «Подготовка доработки»): remark crops cut from the previous
// version's pages, uploaded and referenced from REVISION-<n>.md.
//
// Crops live in revision-crops/<n>/<idx>.png next to the job folder, outside
// input/: a client attachment named remarks/<idx>.png is an ordinary input
// file and can never be mistaken for a prepared crop.
//
// The preparation is idempotent and self-healing: the crops are checked on
// disk, and a missing, empty or truncated crop is rebuilt from the previous
// snapshot again. REVISION-<n>.md is written atomically and references exactly
// the crops that exist, so a continue/answer after an interrupted preparation
// never leaves a link to a file that is not there. Uploads are idempotent
// (same bytes, same key).
func (j *jobExec) prepareRevision(ctx context.Context) error {
	rev := j.asn.Revision
	if rev == nil {
		return fmt.Errorf("нет данных доработки")
	}
	n := rev.Version
	mdPath := filepath.Join(j.dir, fmt.Sprintf("REVISION-%d.md", n))

	cropsDir := revisionCropsDir(j.dir, n)
	if err := os.MkdirAll(cropsDir, 0o755); err != nil {
		return err
	}
	prevDir := filepath.Join(j.snapshotsDir(), fmt.Sprintf("v%d", n-1))
	prevInfo := loadSnapshotInfo(prevDir)

	cropped := map[int]bool{}
	for _, r := range rev.Remarks {
		if j.cancelRequested.Load() {
			return errCanceled
		}
		cropPath := remarkCropPath(cropsDir, r.Idx)
		if !validCrop(cropPath) {
			// Missing, empty or broken: rebuild it from the previous
			// snapshot's page image.
			built, err := j.cropRemark(prevDir, prevInfo, cropsDir, n, r)
			if err != nil {
				// A missing page image must not fail the stage: the remark
				// text and coordinates still reach the agent. The stale crop
				// is dropped so the Markdown never points at a file that is
				// not there.
				j.log.Error("remark crop skipped", "idx", r.Idx, "err", err)
				_ = os.Remove(cropPath)
				continue
			}
			cropPath = built
		}
		if !validCrop(cropPath) {
			_ = os.Remove(cropPath)
			continue
		}
		cropped[r.Idx] = true
		if err := j.uploadCrop(ctx, n, r.Idx, cropPath); err != nil {
			if isStale(err) || errors.Is(err, errCanceled) {
				return err
			}
			j.log.Error("remark crop upload", "idx", r.Idx, "err", err)
		}
	}
	if j.cancelRequested.Load() {
		return errCanceled
	}
	return writeFileAtomic(mdPath, []byte(j.revisionMD(rev, cropped)))
}

// revisionCropsDir is the local folder of the remark crops of version n,
// relative to the job folder. It is deliberately outside input/.
func revisionCropsDir(jobDir string, n int) string {
	return filepath.Join(jobDir, "revision-crops", itoa(n))
}

// remarkCropPath is the local path of a remark crop.
func remarkCropPath(cropsDir string, idx int) string {
	return filepath.Join(cropsDir, fmt.Sprintf("%d.png", idx))
}

// validCrop reports whether the file is a decodable PNG: an empty or
// truncated file left by an interrupted preparation is not.
func validCrop(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err != nil || fi.Size() == 0 {
		return false
	}
	_, err = png.Decode(f)
	return err == nil
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory and a rename, so a reader never sees a half-written file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// cropRemark cuts the remark area (plus a 3 % margin, clamped to the page)
// from the previous snapshot's page image.
func (j *jobExec) cropRemark(prevDir string, prevInfo snapshotInfo, cropsDir string, n int, r httpapi.AssignmentRemark) (string, error) {
	prevIdx := findPrevDoc(prevInfo, r.FilePath)
	if prevIdx < 0 {
		return "", fmt.Errorf("документ %q не найден в снимке v%d", r.FilePath, n-1)
	}
	pagePath := filepath.Join(prevDir, "pages", itoa(prevIdx), fmt.Sprintf("%d.png", r.Page))
	f, err := os.Open(pagePath)
	if err != nil {
		return "", err
	}
	img, err := png.Decode(f)
	_ = f.Close()
	if err != nil {
		return "", err
	}

	b := img.Bounds()
	W, H := float64(b.Dx()), float64(b.Dy())
	mx, my := 0.03*W, 0.03*H
	x0 := clampF(float64(r.X)*W-mx, 0, W-1)
	y0 := clampF(float64(r.Y)*H-my, 0, H-1)
	x1 := clampF(float64(r.X+r.W)*W+mx, x0+1, W)
	y1 := clampF(float64(r.Y+r.H)*H+my, y0+1, H)
	rect := image.Rect(int(math.Round(x0)), int(math.Round(y0)), int(math.Round(x1)), int(math.Round(y1)))

	type cropper interface {
		SubImage(image.Rectangle) image.Image
	}
	sub, ok := img.(cropper)
	if !ok {
		return "", fmt.Errorf("картинка не поддерживает вырезку")
	}
	dst := remarkCropPath(cropsDir, r.Idx)
	// Write through a temporary file: an interrupted crop never overwrites a
	// good one with a truncated PNG.
	tmp, err := os.CreateTemp(cropsDir, ".crop-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if err := png.Encode(tmp, sub.SubImage(rect)); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	return dst, nil
}

func (j *jobExec) uploadCrop(ctx context.Context, n, idx int, path string) error {
	return j.putWithRetryStop(ctx, path, func(r io.Reader) error {
		return j.w.cl.PutRevisionRemark(ctx, j.asn.JobId, j.asn.Epoch, n, idx, r)
	})
}

// revisionMD renders REVISION-<n>.md (05-worker.md «Подготовка доработки»).
// The «Вырезка: …» reference is written only for remarks whose crop exists.
func (j *jobExec) revisionMD(rev *httpapi.AssignmentRevision, cropped map[int]bool) string {
	n := rev.Version
	var b strings.Builder
	fmt.Fprintf(&b, "# Доработка — версия %d\n\n", n)
	comment := strings.TrimSpace(rev.Comment)
	if comment == "" {
		comment = "—"
	}
	fmt.Fprintf(&b, "Комментарий клиента: %s\n\n", comment)
	b.WriteString("## Замечания на листах\n\n")
	if len(rev.Remarks) == 0 {
		b.WriteString("—\n")
	}
	for _, r := range rev.Remarks {
		fmt.Fprintf(&b, "%d. «%s» — %s (%s), стр. %d, область: x=%.3f, y=%.3f, w=%.3f, h=%.3f (доли листа, от левого верхнего угла).",
			r.Idx, r.Text, r.DocumentTitle, r.FilePath, r.Page, r.X, r.Y, r.W, r.H)
		if cropped[r.Idx] {
			fmt.Fprintf(&b, " Вырезка: revision-crops/%d/%d.png", n, r.Idx)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n## Приложенные файлы\n\n")
	if len(rev.Files) == 0 {
		b.WriteString("—\n")
	}
	for _, f := range rev.Files {
		fmt.Fprintf(&b, "- input/%s\n", j.localPath(strings.TrimPrefix(f.Path, "input/")))
	}
	return b.String()
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
