// Package preview renders document previews for worker snapshots
// (05-worker.md «Снимок версии»): PDF pages via pdftoppm, thumbnails,
// per-page diff boxes and office-to-PDF conversion.
package preview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/image/draw"
)

// Box is a rectangular region of a page, in fractions 0–1 of the current
// page dimensions (from the top-left corner).
type Box struct {
	X, Y, W, H float64
}

// Renderer rasterizes PDFs into per-page PNGs. PdfToppm overrides the
// pdftoppm binary path; empty means "pdftoppm" from PATH.
type Renderer struct {
	PdfToppm string
}

// RenderPDF runs `pdftoppm -r 110 -png <pdfPath> <outDir>/p` and returns the
// generated page paths ordered by page number (pdftoppm pads numbers with
// zeros depending on the page count, so the sort is numeric, not lexical).
func (r Renderer) RenderPDF(ctx context.Context, pdfPath, outDir string) ([]string, error) {
	tool := r.PdfToppm
	if tool == "" {
		tool = "pdftoppm"
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("preview: mkdir %s: %w", outDir, err)
	}
	prefix := filepath.Join(outDir, "p")
	cmd := exec.CommandContext(ctx, tool, "-r", "110", "-png", pdfPath, prefix)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("preview: pdftoppm: %w: %s", err, strings.TrimSpace(string(out)))
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		return nil, fmt.Errorf("preview: read %s: %w", outDir, err)
	}
	type page struct {
		num  int
		path string
	}
	var pages []page
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "p-") || !strings.HasSuffix(name, ".png") {
			continue
		}
		num, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "p-"), ".png"))
		if err != nil {
			continue
		}
		pages = append(pages, page{num: num, path: filepath.Join(outDir, name)})
	}
	sort.Slice(pages, func(i, j int) bool {
		if pages[i].num != pages[j].num {
			return pages[i].num < pages[j].num
		}
		return pages[i].path < pages[j].path
	})
	paths := make([]string, len(pages))
	for i, p := range pages {
		paths[i] = p.path
	}
	return paths, nil
}

var (
	pngMagic  = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	jpegMagic = []byte{0xFF, 0xD8, 0xFF}
)

// sniffFormat reports the image format ("png" or "jpeg") by magic bytes.
func sniffFormat(head []byte) (string, error) {
	switch {
	case bytes.HasPrefix(head, pngMagic):
		return "png", nil
	case bytes.HasPrefix(head, jpegMagic):
		return "jpeg", nil
	default:
		return "", errors.New("preview: unsupported image format (want PNG or JPEG)")
	}
}

// decodeImage loads a PNG or JPEG without relying on registered formats.
func decodeImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, len(pngMagic))
	if _, err := io.ReadFull(f, head); err != nil {
		return nil, fmt.Errorf("preview: read %s: %w", path, err)
	}
	format, err := sniffFormat(head)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("preview: seek %s: %w", path, err)
	}
	switch format {
	case "png":
		return png.Decode(f)
	default:
		return jpeg.Decode(f)
	}
}

// ImageSize returns the dimensions of a PNG or JPEG without a full decode.
func ImageSize(path string) (w, h int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	head := make([]byte, len(pngMagic))
	if _, err := io.ReadFull(f, head); err != nil {
		return 0, 0, fmt.Errorf("preview: read %s: %w", path, err)
	}
	format, err := sniffFormat(head)
	if err != nil {
		return 0, 0, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, 0, fmt.Errorf("preview: seek %s: %w", path, err)
	}
	var cfg image.Config
	switch format {
	case "png":
		cfg, err = png.DecodeConfig(f)
	default:
		cfg, err = jpeg.DecodeConfig(f)
	}
	if err != nil {
		return 0, 0, fmt.Errorf("preview: decode config %s: %w", path, err)
	}
	return cfg.Width, cfg.Height, nil
}

// thumbWidth is the thumbnail width from 05-worker.md «Снимок версии» п.4.
const thumbWidth = 240

// WriteThumb writes a thumbnail of srcPath (scaled to thumbWidth pixels wide,
// CatmullRom) as PNG to dstPath. Images already narrower than thumbWidth are
// copied as-is.
func WriteThumb(srcPath, dstPath string) error {
	src, err := decodeImage(srcPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	b := src.Bounds()
	if b.Dx() <= thumbWidth {
		return copyFile(srcPath, dstPath)
	}
	h := max(b.Dy()*thumbWidth/b.Dx(), 1)
	dst := image.NewRGBA(image.Rect(0, 0, thumbWidth, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)

	out, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	if err := png.Encode(out, dst); err != nil {
		out.Close()
		return fmt.Errorf("preview: encode %s: %w", dstPath, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("preview: write %s: %w", dstPath, err)
	}
	return nil
}

func copyFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return fmt.Errorf("preview: copy to %s: %w", dstPath, err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("preview: write %s: %w", dstPath, err)
	}
	return nil
}

// Diff parameters from 05-worker.md «Снимок версии» п.5.
const (
	diffWidth  = 400 // both pages are scaled to this width before comparing
	diffCols   = 40
	diffThresh = 6  // mean abs gray difference per cell, out of 255
	diffMaxBox = 30 // more changed regions than this collapses to a full-page box
)

// DiffPages compares the current page with the same page of the previous
// snapshot and returns changed regions in fractions 0–1 of the current page.
// A missing or unreadable previous page yields a single full-page box.
func DiffPages(prevPath, curPath string) ([]Box, error) {
	full := []Box{{0, 0, 1, 1}}

	cur, err := decodeImage(curPath)
	if err != nil {
		return nil, err
	}
	if prevPath == "" {
		return full, nil
	}
	prev, err := decodeImage(prevPath)
	if err != nil {
		return full, nil
	}

	prevG := scaleGray(prev, diffWidth)
	curG := scaleGray(cur, diffWidth)

	cell := diffWidth / diffCols
	curH := curG.Bounds().Dy()
	prevH := prevG.Bounds().Dy()
	rows := (curH + cell - 1) / cell

	changed := make([]bool, rows*diffCols)
	for r := range rows {
		y0 := r * cell
		y1 := min(y0+cell, curH)
		for c := range diffCols {
			x0 := c * cell
			x1 := min(x0+cell, diffWidth)
			// Cells reaching past the previous page are changed by
			// definition (zones outside the smaller image).
			if y1 > prevH {
				changed[r*diffCols+c] = true
				continue
			}
			if meanAbsDiff(prevG, curG, x0, y0, x1, y1) > diffThresh {
				changed[r*diffCols+c] = true
			}
		}
	}

	boxes := collectBoxes(changed, cell, curH)
	if len(boxes) > diffMaxBox {
		return full, nil
	}
	return boxes, nil
}

// scaleGray scales src to width w (CatmullRom) and converts to grayscale.
func scaleGray(src image.Image, w int) *image.Gray {
	b := src.Bounds()
	h := max(b.Dy()*w/b.Dx(), 1)
	dst := image.NewGray(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// meanAbsDiff returns the mean absolute gray difference over the rectangle
// on the 0–255 scale, without rounding: a mean of 6.4 is above diffThresh.
func meanAbsDiff(a, b *image.Gray, x0, y0, x1, y1 int) float64 {
	var sum, n int
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			d := int(a.GrayAt(x, y).Y) - int(b.GrayAt(x, y).Y)
			if d < 0 {
				d = -d
			}
			sum += d
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(sum) / float64(n)
}

// collectBoxes merges changed cells into 4-connected regions and returns the
// bounding box of each region in fractions 0–1 of the page (X/W of its
// width, Y/H of its height; the last cell row can be shorter than cellH).
func collectBoxes(changed []bool, cellH, pageH int) []Box {
	visited := make([]bool, len(changed))
	var boxes []Box
	for i, ch := range changed {
		if !ch || visited[i] {
			continue
		}
		// BFS over 4-connected changed cells.
		minC, maxC := i%diffCols, i%diffCols
		minR, maxR := i/diffCols, i/diffCols
		queue := []int{i}
		visited[i] = true
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			r, c := cur/diffCols, cur%diffCols
			minC, maxC = min(minC, c), max(maxC, c)
			minR, maxR = min(minR, r), max(maxR, r)
			for _, next := range [4]int{cur - diffCols, cur + diffCols, cur - 1, cur + 1} {
				if next < 0 || next >= len(changed) {
					continue
				}
				// Guard against row wrap-around for the left/right moves.
				if (next == cur-1 || next == cur+1) && next/diffCols != r {
					continue
				}
				if changed[next] && !visited[next] {
					visited[next] = true
					queue = append(queue, next)
				}
			}
		}
		y0 := minR * cellH
		y1 := min((maxR+1)*cellH, pageH)
		boxes = append(boxes, Box{
			X: float64(minC) / diffCols,
			Y: float64(y0) / float64(pageH),
			W: float64(maxC-minC+1) / diffCols,
			H: float64(y1-y0) / float64(pageH),
		})
	}
	return boxes
}
