package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	workTitle  = "Практическая работа: расчёт балки"
	docxName   = "Пояснительная записка.docx"
	xlsxName   = "Расчёт.xlsx"
	pageCount  = 2
	draftText  = "Practical work draft page %d"
	verifyText = "Practical work verified page %d (fixed)"
)

// writeDraftArtifacts creates the draft workspace: out/ documents, preview/
// PDFs (unless disabled), manifest.json and SUMMARY.md. It returns the list
// of written file paths relative to dir.
func writeDraftArtifacts(dir string, withPreview bool) ([]string, error) {
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		return nil, err
	}
	files := []string{"out/" + docxName, "out/" + xlsxName}
	if err := writeDocx(filepath.Join(dir, "out", docxName)); err != nil {
		return nil, err
	}
	if err := writeXlsx(filepath.Join(dir, "out", xlsxName)); err != nil {
		return nil, err
	}

	previews := []string{
		"preview/" + strings.TrimSuffix(docxName, ".docx") + ".pdf",
		"preview/" + strings.TrimSuffix(xlsxName, ".xlsx") + ".pdf",
	}
	if withPreview {
		if err := os.MkdirAll(filepath.Join(dir, "preview"), 0o755); err != nil {
			return nil, err
		}
		for _, rel := range previews {
			pages := make([]string, pageCount)
			for i := range pages {
				pages[i] = fmt.Sprintf(draftText, i+1)
			}
			if err := writePDF(filepath.Join(dir, rel), pages); err != nil {
				return nil, err
			}
			files = append(files, rel)
		}
	}

	if err := writeManifest(dir, withPreview); err != nil {
		return nil, err
	}
	files = append(files, "manifest.json")

	summary := workTitle + "\n\n" +
		"Выполнен расчёт однопролётной шарнирно опертой балки: построены эпюры " +
		"внутренних усилий, подобрано сечение по условиям прочности и жёсткости. " +
		"Пояснения — в записке, численные результаты — в таблице расчёта.\n"
	if err := os.WriteFile(filepath.Join(dir, "SUMMARY.md"), []byte(summary), 0o644); err != nil {
		return nil, err
	}
	files = append(files, "SUMMARY.md")
	return files, nil
}

// manifest is the structure the worker reads after each stage.
type manifest struct {
	Title     string             `json:"title"`
	Documents []manifestDocument `json:"documents"`
}

type manifestDocument struct {
	File    string `json:"file"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Preview string `json:"preview,omitempty"`
}

// writeManifest writes manifest.json for the draft artifacts.
func writeManifest(dir string, withPreview bool) error {
	previewOf := func(name string) string {
		if !withPreview {
			return ""
		}
		return "preview/" + name + ".pdf"
	}
	m := manifest{
		Title: workTitle,
		Documents: []manifestDocument{
			{
				File:    "out/" + docxName,
				Title:   "Пояснительная записка",
				Kind:    "Word",
				Preview: previewOf("Пояснительная записка"),
			},
			{
				File:    "out/" + xlsxName,
				Title:   "Расчёт балки",
				Kind:    "Excel",
				Preview: previewOf("Расчёт"),
			},
		},
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), append(data, '\n'), 0o644)
}

// verifyPreviews rewrites every preview PDF with the "verified" page texts
// (page 2 differs from the draft text, which is what changed_boxes needs).
func verifyPreviews(dir string) error {
	names, err := previewNames(dir)
	if err != nil || len(names) == 0 {
		return err
	}
	for _, name := range names {
		pages := make([]string, pageCount)
		for i := range pages {
			pages[i] = fmt.Sprintf(verifyText, i+1)
		}
		if err := writePDF(filepath.Join(dir, "preview", name), pages); err != nil {
			return err
		}
	}
	return nil
}

// revisePreviews rewrites the preview PDFs, changing only the pages listed
// in the revision (1-based).
func revisePreviews(dir string, n int, changed []int) error {
	names, err := previewNames(dir)
	if err != nil || len(names) == 0 {
		return err
	}
	onList := map[int]bool{}
	for _, p := range changed {
		onList[p] = true
	}
	for _, name := range names {
		pages := make([]string, pageCount)
		for i := range pages {
			if onList[i+1] {
				pages[i] = fmt.Sprintf("Practical work revised page %d (revision %d)", i+1, n)
			} else {
				pages[i] = fmt.Sprintf(verifyText, i+1)
			}
		}
		if err := writePDF(filepath.Join(dir, "preview", name), pages); err != nil {
			return err
		}
	}
	return nil
}

// previewNames lists the PDF files in preview/, or nil when there is no
// preview folder (a #no-pdf draft).
func previewNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "preview"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pdf") {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// verificationFile is the verification.json structure written by claude.
type verificationFile struct {
	Found     int `json:"found"`
	Fixed     int `json:"fixed"`
	Remaining []struct {
		Severity    string `json:"severity"`
		Description string `json:"description"`
	} `json:"remaining"`
}

// writeVerification writes VERIFICATION.md and verification.json.
func writeVerification(dir string, found, fixed int) error {
	md := "# Проверка работы\n\n" +
		fmt.Sprintf("Найдено замечаний: %d, исправлено: %d.\n\n", found, fixed) +
		"Проверены расчётные формулы, оформление записки и значения в таблице. " +
		"Незакрытых замечаний не осталось.\n"
	if err := os.WriteFile(filepath.Join(dir, "VERIFICATION.md"), []byte(md), 0o644); err != nil {
		return err
	}
	return writeVerificationJSON(dir, verificationFile{Found: found, Fixed: fixed, Remaining: nil})
}

func writeVerificationJSON(dir string, v verificationFile) error {
	if v.Remaining == nil {
		v.Remaining = []struct {
			Severity    string `json:"severity"`
			Description string `json:"description"`
		}{}
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "verification.json"), append(data, '\n'), 0o644)
}

// appendRevisionNote adds a "## Доработка <n>" section to VERIFICATION.md.
func appendRevisionNote(dir string, n int, pages []int) error {
	f, err := os.OpenFile(filepath.Join(dir, "VERIFICATION.md"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n## Доработка %d\n\nИсправлены замечания на страницах: %s.\n", n, joinInts(pages))
	return err
}

// bumpVerification increments found/fixed in verification.json by delta.
func bumpVerification(dir string, delta int) error {
	v := verificationFile{}
	if data, err := os.ReadFile(filepath.Join(dir, "verification.json")); err == nil {
		_ = json.Unmarshal(data, &v)
	}
	v.Found += delta
	v.Fixed += delta
	return writeVerificationJSON(dir, v)
}

func joinInts(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ", ")
}

// --- office documents -------------------------------------------------------

func writeZip(path string, files map[string]string) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	for _, name := range []string{
		"[Content_Types].xml", "_rels/.rels", "word/document.xml",
		"xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/worksheets/sheet1.xml",
	} {
		body, ok := files[name]
		if !ok {
			continue
		}
		w, err := zw.Create(name)
		if err != nil {
			zw.Close()
			out.Close()
			return err
		}
		if _, err := w.Write([]byte(body)); err != nil {
			zw.Close()
			out.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

const xmlDecl = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// writeDocx writes a minimal valid .docx (one paragraph).
func writeDocx(path string) error {
	return writeZip(path, map[string]string{
		"[Content_Types].xml": xmlDecl +
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
			`</Types>`,
		"_rels/.rels": xmlDecl +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
			`</Relationships>`,
		"word/document.xml": xmlDecl +
			`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
			`<w:p><w:r><w:t>Practical work: calculation of a simply supported beam.</w:t></w:r></w:p>` +
			`<w:sectPr/></w:body></w:document>`,
	})
}

// writeXlsx writes a minimal valid .xlsx (one sheet, one cell).
func writeXlsx(path string) error {
	return writeZip(path, map[string]string{
		"[Content_Types].xml": xmlDecl +
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
			`</Types>`,
		"_rels/.rels": xmlDecl +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
			`</Relationships>`,
		"xl/workbook.xml": xmlDecl +
			`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
			`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
			`<sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": xmlDecl +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
			`</Relationships>`,
		"xl/worksheets/sheet1.xml": xmlDecl +
			`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` +
			`<row r="1"><c r="A1" t="inlineStr"><is><t>Beam calculation results</t></is></c></row>` +
			`</sheetData></worksheet>`,
	})
}
