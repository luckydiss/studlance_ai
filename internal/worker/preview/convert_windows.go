//go:build windows

package preview

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// convertTimeout bounds a single office-to-PDF conversion (05-worker.md).
const convertTimeout = 3 * time.Minute

// convertScript converts an office document to PDF via COM automation.
// Usage: powershell.exe -NoProfile -ExecutionPolicy Bypass -File <ps1> <in> <out> <app>.
const convertScript = `param([string]$in, [string]$out, [string]$app)

$ErrorActionPreference = "Stop"

switch ($app) {
  "word" {
    $word = New-Object -ComObject Word.Application
    $word.Visible = $false
    try {
      $doc = $word.Documents.Open($in, $false, $true)
      try {
        $doc.SaveAs([ref]$out, [ref]17)
      } finally {
        $doc.Close($false)
      }
    } finally {
      $word.Quit()
    }
  }
  "excel" {
    $excel = New-Object -ComObject Excel.Application
    $excel.Visible = $false
    $excel.DisplayAlerts = $false
    try {
      $book = $excel.Workbooks.Open($in)
      try {
        $book.ExportAsFixedFormat(0, $out)
      } finally {
        $book.Close($false)
      }
    } finally {
      $excel.Quit()
    }
  }
  "powerpoint" {
    $ppt = New-Object -ComObject PowerPoint.Application
    try {
      $pres = $ppt.Presentations.Open($in, $true, $false, $false)
      try {
        $pres.SaveAs($out, 32)
      } finally {
        $pres.Close()
      }
    } finally {
      $ppt.Quit()
    }
  }
  default {
    Write-Error "unknown app: $app"
    exit 1
  }
}
`

// convertApps maps supported file extensions to COM applications.
var convertApps = map[string]string{
	".docx": "word",
	".doc":  "word",
	".rtf":  "word",
	".odt":  "word",
	".xlsx": "excel",
	".xls":  "excel",
	".pptx": "powerpoint",
}

// ConvertToPDF converts an office document to PDF via Office COM automation
// (05-worker.md «Снимок версии» п.3). It reports false for unsupported
// extensions; the 3-minute timeout applies per file.
func ConvertToPDF(ctx context.Context, srcPath, dstPath string) (bool, error) {
	app, ok := convertApps[strings.ToLower(filepath.Ext(srcPath))]
	if !ok {
		return false, nil
	}

	ps1, err := os.CreateTemp("", "studlance-convert-*.ps1")
	if err != nil {
		return false, fmt.Errorf("preview: create script: %w", err)
	}
	defer os.Remove(ps1.Name())
	if _, err := ps1.WriteString(convertScript); err != nil {
		ps1.Close()
		return false, fmt.Errorf("preview: write script: %w", err)
	}
	if err := ps1.Close(); err != nil {
		return false, fmt.Errorf("preview: write script: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, convertTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", ps1.Name(), srcPath, dstPath, app)
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("preview: convert %s: %w: %s", srcPath, err, strings.TrimSpace(string(out)))
	}

	fi, err := os.Stat(dstPath)
	if err != nil {
		return false, fmt.Errorf("preview: convert %s: no output: %w", srcPath, err)
	}
	if fi.Size() == 0 {
		return false, fmt.Errorf("preview: convert %s: empty output %s", srcPath, dstPath)
	}
	return true, nil
}
