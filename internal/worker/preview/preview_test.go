package preview

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writePNG encodes img to path.
func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// solidPNG writes a w×h PNG filled with c, with an optional black rectangle.
func solidPNG(t *testing.T, path string, w, h int, c color.RGBA, rect image.Rectangle) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	black := color.RGBA{0, 0, 0, 255}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			img.SetRGBA(x, y, black)
		}
	}
	writePNG(t, path, img)
}

func TestImageSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.png")
	solidPNG(t, path, 320, 200, color.RGBA{255, 255, 255, 255}, image.Rectangle{})

	w, h, err := ImageSize(path)
	if err != nil {
		t.Fatal(err)
	}
	if w != 320 || h != 200 {
		t.Fatalf("got %dx%d, want 320x200", w, h)
	}
}

func TestWriteThumb(t *testing.T) {
	dir := t.TempDir()

	big := filepath.Join(dir, "big.png")
	solidPNG(t, big, 480, 240, color.RGBA{255, 255, 255, 255}, image.Rectangle{})
	bigThumb := filepath.Join(dir, "big-thumb.png")
	if err := WriteThumb(big, bigThumb); err != nil {
		t.Fatal(err)
	}
	w, h, err := ImageSize(bigThumb)
	if err != nil {
		t.Fatal(err)
	}
	if w != 240 || h != 120 {
		t.Fatalf("got %dx%d, want 240x120", w, h)
	}

	small := filepath.Join(dir, "small.png")
	solidPNG(t, small, 100, 50, color.RGBA{255, 255, 255, 255}, image.Rectangle{})
	smallThumb := filepath.Join(dir, "small-thumb.png")
	if err := WriteThumb(small, smallThumb); err != nil {
		t.Fatal(err)
	}
	w, h, err = ImageSize(smallThumb)
	if err != nil {
		t.Fatal(err)
	}
	if w != 100 || h != 50 {
		t.Fatalf("got %dx%d, want 100x50 (copied as-is)", w, h)
	}
}

func TestDiffPagesIdentical(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "p.png")
	solidPNG(t, page, 400, 300, color.RGBA{255, 255, 255, 255}, image.Rectangle{})

	boxes, err := DiffPages(page, page)
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 0 {
		t.Fatalf("got %d boxes, want 0: %v", len(boxes), boxes)
	}
}

func TestDiffPagesFullyDifferent(t *testing.T) {
	dir := t.TempDir()
	prev := filepath.Join(dir, "prev.png")
	cur := filepath.Join(dir, "cur.png")
	solidPNG(t, prev, 400, 300, color.RGBA{255, 255, 255, 255}, image.Rectangle{})
	solidPNG(t, cur, 400, 300, color.RGBA{0, 0, 0, 255}, image.Rectangle{})

	boxes, err := DiffPages(prev, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 1 {
		t.Fatalf("got %d boxes, want 1: %v", len(boxes), boxes)
	}
	if boxes[0] != (Box{0, 0, 1, 1}) {
		t.Fatalf("got %+v, want {0 0 1 1}", boxes[0])
	}
}

func TestDiffPagesLocalSpot(t *testing.T) {
	dir := t.TempDir()
	prev := filepath.Join(dir, "prev.png")
	cur := filepath.Join(dir, "cur.png")
	solidPNG(t, prev, 400, 300, color.RGBA{255, 255, 255, 255}, image.Rectangle{})
	// Spot at x=100..150, y=60..90 → cells 10..14 of 40 cols, rows 6..8 of 30.
	spot := image.Rect(100, 60, 150, 90)
	solidPNG(t, cur, 400, 300, color.RGBA{255, 255, 255, 255}, spot)

	boxes, err := DiffPages(prev, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 1 {
		t.Fatalf("got %d boxes, want 1: %v", len(boxes), boxes)
	}
	b := boxes[0]
	// Tolerance: one cell (1/40 in X, 1/30 in Y).
	const tolX, tolY = 1.0 / 40, 1.0 / 30
	want := Box{0.25, 0.2, 0.125, 0.1}
	if b.X < want.X-tolX || b.X > want.X+tolX ||
		b.Y < want.Y-tolY || b.Y > want.Y+tolY ||
		b.W < want.W-tolX || b.W > want.W+tolX ||
		b.H < want.H-tolY || b.H > want.H+tolY {
		t.Fatalf("got %+v, want ~%+v", b, want)
	}
}

func TestDiffPagesNoPrev(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "cur.png")
	solidPNG(t, cur, 400, 300, color.RGBA{255, 255, 255, 255}, image.Rectangle{})

	for _, prev := range []string{"", filepath.Join(dir, "missing.png")} {
		boxes, err := DiffPages(prev, cur)
		if err != nil {
			t.Fatal(err)
		}
		if len(boxes) != 1 || boxes[0] != (Box{0, 0, 1, 1}) {
			t.Fatalf("prev=%q: got %v, want [{0 0 1 1}]", prev, boxes)
		}
	}
}

// minimalPDF returns a valid one-page PDF 1.4 document (72×72 pt blank page).
func minimalPDF(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	buf.WriteString("%PDF-1.4\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 72 72] >>")
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(offsets)+1)
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return buf.Bytes()
}

func TestRenderPDF(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not in PATH")
	}
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdfPath, minimalPDF(t), 0o644); err != nil {
		t.Fatal(err)
	}

	pages, err := Renderer{}.RenderPDF(context.Background(), pdfPath, filepath.Join(dir, "pages"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 {
		t.Fatalf("got %d pages, want 1: %v", len(pages), pages)
	}
	w, h, err := ImageSize(pages[0])
	if err != nil {
		t.Fatal(err)
	}
	// 72 pt at 110 dpi → 110 px.
	if w != 110 || h != 110 {
		t.Fatalf("got %dx%d, want 110x110", w, h)
	}
}
