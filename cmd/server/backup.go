package main

import (
	"archive/zip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/luckydiss/studlance_ai/internal/config"

	_ "modernc.org/sqlite"
)

// backup writes a zip containing a consistent SQLite copy (VACUUM INTO) and
// the blobs folder.
func backup(ctx context.Context, cfg config.Server, out string) error {
	dbPath := filepath.Join(cfg.Data, "studlance.db")
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("database not found at %s: %w", dbPath, err)
	}

	tmpDir, err := os.MkdirTemp("", "studlance-backup-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tmpDB := filepath.Join(tmpDir, "studlance.db")
	if err := vacuumInto(ctx, dbPath, tmpDB); err != nil {
		return fmt.Errorf("vacuum into: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	zw := zip.NewWriter(f)
	if err := addFile(zw, tmpDB, "studlance.db"); err != nil {
		_ = zw.Close()
		return err
	}
	blobsDir := filepath.Join(cfg.Data, "blobs")
	if _, err := os.Stat(blobsDir); err == nil {
		if err := addTree(zw, blobsDir, "blobs"); err != nil {
			_ = zw.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	fmt.Printf("backup written: %s\n", out)
	return nil
}

func vacuumInto(ctx context.Context, src, dst string) error {
	db, err := sql.Open("sqlite", "file:"+src)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(ctx, "VACUUM INTO ?", dst)
	return err
}

func addFile(zw *zip.Writer, path, name string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, src)
	return err
}

func addTree(zw *zip.Writer, root, base string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		return addFile(zw, p, filepath.ToSlash(filepath.Join(base, rel)))
	})
}
