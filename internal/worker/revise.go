package worker

import (
	"context"
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
// version's pages, uploaded and referenced from REVISION-<n>.md. Idempotent:
// a fully prepared revision (REVISION-<n>.md exists) is not rebuilt.
func (j *jobExec) prepareRevision(ctx context.Context) error {
	rev := j.asn.Revision
	if rev == nil {
		return fmt.Errorf("нет данных доработки")
	}
	n := rev.Version
	mdPath := filepath.Join(j.dir, fmt.Sprintf("REVISION-%d.md", n))
	if _, err := os.Stat(mdPath); err == nil {
		return nil // already prepared (e.g. continue after a crash mid-stage)
	}

	remarksDir := filepath.Join(j.dir, "input", fmt.Sprintf("revision-%d", n), "remarks")
	if err := os.MkdirAll(remarksDir, 0o755); err != nil {
		return err
	}
	prevDir := filepath.Join(j.snapshotsDir(), fmt.Sprintf("v%d", n-1))
	prevInfo := loadSnapshotInfo(prevDir)

	cropped := map[int]bool{}
	for _, r := range rev.Remarks {
		cropPath, err := j.cropRemark(prevDir, prevInfo, remarksDir, n, r)
		if err != nil {
			// A missing page image must not fail the stage: the remark text
			// and coordinates still reach the agent.
			j.log.Error("remark crop skipped", "idx", r.Idx, "err", err)
			continue
		}
		cropped[r.Idx] = true
		if err := j.uploadCrop(ctx, n, r.Idx, cropPath); err != nil {
			if isStale(err) {
				return err
			}
			j.log.Error("remark crop upload", "idx", r.Idx, "err", err)
		}
	}

	return os.WriteFile(mdPath, []byte(j.revisionMD(rev, cropped)), 0o644)
}

// cropRemark cuts the remark area (plus a 3 % margin, clamped to the page)
// from the previous snapshot's page image.
func (j *jobExec) cropRemark(prevDir string, prevInfo snapshotInfo, remarksDir string, n int, r httpapi.AssignmentRemark) (string, error) {
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
	dst := filepath.Join(remarksDir, fmt.Sprintf("%d.png", r.Idx))
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer func() { _ = out.Close() }()
	if err := png.Encode(out, sub.SubImage(rect)); err != nil {
		return "", err
	}
	return dst, nil
}

func (j *jobExec) uploadCrop(ctx context.Context, n, idx int, path string) error {
	return j.putWithRetry(ctx, path, func(r io.Reader) error {
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
			fmt.Fprintf(&b, " Вырезка: input/revision-%d/remarks/%d.png", n, r.Idx)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n## Приложенные файлы\n\n")
	if len(rev.Files) == 0 {
		b.WriteString("—\n")
	}
	for _, f := range rev.Files {
		p := f.Path
		if !strings.HasPrefix(p, "input/") {
			p = "input/" + p
		}
		fmt.Fprintf(&b, "- %s\n", p)
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
