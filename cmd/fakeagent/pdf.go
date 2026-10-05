package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writePDF writes a minimal, valid PDF 1.4 document with one line of ASCII
// text per page (Helvetica). The xref table is computed from exact byte
// offsets so strict readers (poppler/pdftoppm) accept the file.
func writePDF(path string, pages []string) error {
	if len(pages) == 0 {
		pages = []string{""}
	}
	fontObj := 3 + 2*len(pages)

	var buf bytes.Buffer
	offsets := make([]int, fontObj+1) // indexed by object number
	obj := func(num int, body string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}

	buf.WriteString("%PDF-1.4\n")

	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 3+2*i)
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	for i, text := range pages {
		content := fmt.Sprintf("BT /F1 18 Tf 72 770 Td (%s) Tj ET", escapePDFText(text))
		obj(3+2*i, fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] "+
				"/Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			fontObj, 4+2*i))
		obj(4+2*i, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}
	obj(fontObj, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	startXref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", fontObj+1)
	buf.WriteString("0000000000 65535 f \n")
	for num := 1; num <= fontObj; num++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[num])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", fontObj+1, startXref)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// escapePDFText escapes the characters that are special inside a PDF string.
func escapePDFText(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `(`, `\(`)
	s = strings.ReplaceAll(s, `)`, `\)`)
	return s
}
