package fs_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckydiss/studlance_ai/internal/blobs"
	blobfs "github.com/luckydiss/studlance_ai/internal/blobs/fs"
)

func newBlobs(t *testing.T) *blobfs.Blobs {
	t.Helper()
	b, err := blobfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return b
}

func TestPutOpenRoundtrip(t *testing.T) {
	ctx := context.Background()
	b := newBlobs(t)

	data := []byte("hello мир")
	size, sum, err := b.Put(ctx, "jobs/j1/input/файл.txt", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if size != int64(len(data)) {
		t.Fatalf("size %d want %d", size, len(data))
	}
	if len(sum) != 64 {
		t.Fatalf("sha256 %q", sum)
	}

	rc, info, err := b.Open(ctx, "jobs/j1/input/файл.txt")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Fatalf("got %q want %q", got, data)
	}
	if info.Size != int64(len(data)) {
		t.Fatalf("info size %d", info.Size)
	}
}

func TestPutIsAtomicReplaces(t *testing.T) {
	ctx := context.Background()
	b := newBlobs(t)

	if _, _, err := b.Put(ctx, "a/b.txt", strings.NewReader("first")); err != nil {
		t.Fatalf("put1: %v", err)
	}
	if _, _, err := b.Put(ctx, "a/b.txt", strings.NewReader("second-longer")); err != nil {
		t.Fatalf("put2: %v", err)
	}
	rc, _, err := b.Open(ctx, "a/b.txt")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, _ := io.ReadAll(rc)
	if string(got) != "second-longer" {
		t.Fatalf("got %q", got)
	}

	// No temp files left behind in the directory.
	entries, err := os.ReadDir(filepath.Join(b.Root(), "a"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestRejectsBadKeys(t *testing.T) {
	ctx := context.Background()
	b := newBlobs(t)

	bad := []string{
		"../escape.txt",
		"a/../../escape.txt",
		"/absolute.txt",
		`a\b.txt`,
		"",
		"a//b.txt",
		"a/./b.txt",
		"a/\x01b.txt",
	}
	for _, key := range bad {
		if _, _, err := b.Put(ctx, key, strings.NewReader("x")); !errors.Is(err, blobs.ErrInvalidKey) {
			t.Fatalf("key %q: got %v want ErrInvalidKey", key, err)
		}
	}
}

func TestDeleteAndList(t *testing.T) {
	ctx := context.Background()
	b := newBlobs(t)
	for _, k := range []string{"p/1.txt", "p/2.txt", "q/3.txt"} {
		if _, _, err := b.Put(ctx, k, strings.NewReader("x")); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	list, err := b.List(ctx, "p")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list len %d want 2", len(list))
	}
	if err := b.Delete(ctx, "p/1.txt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := b.Delete(ctx, "p/1.txt"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if _, _, err := b.Open(ctx, "p/1.txt"); !errors.Is(err, blobs.ErrNotFound) {
		t.Fatalf("open deleted: %v", err)
	}
}
