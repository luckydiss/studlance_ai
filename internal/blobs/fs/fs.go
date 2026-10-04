// Package fs implements blobs.Blobs as a folder on disk.
package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/luckydiss/studlance_ai/internal/blobs"
)

// Blobs is a filesystem-backed blob store rooted at root.
type Blobs struct {
	root string
}

// New creates a filesystem blob store rooted at root.
func New(root string) (*Blobs, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &Blobs{root: abs}, nil
}

// Root returns the absolute root directory.
func (b *Blobs) Root() string { return b.root }

// ValidateKey rejects absolute paths, backslashes, ".." and control chars.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty", blobs.ErrInvalidKey)
	}
	if strings.ContainsRune(key, '\\') {
		return fmt.Errorf("%w: contains backslash", blobs.ErrInvalidKey)
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("%w: absolute", blobs.ErrInvalidKey)
	}
	if strings.Contains(key, "//") {
		return fmt.Errorf("%w: empty segment", blobs.ErrInvalidKey)
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: bad segment %q", blobs.ErrInvalidKey, part)
		}
		for _, r := range part {
			if r < 0x20 {
				return fmt.Errorf("%w: control char", blobs.ErrInvalidKey)
			}
		}
	}
	return nil
}

func (b *Blobs) path(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	return filepath.Join(b.root, filepath.FromSlash(key)), nil
}

// Put atomically writes r under key.
func (b *Blobs) Put(ctx context.Context, key string, r io.Reader) (int64, string, error) {
	dst, err := b.path(key)
	if err != nil {
		return 0, "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return 0, "", err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		_ = tmp.Close()
		return 0, "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return 0, "", err
	}
	if err := tmp.Close(); err != nil {
		return 0, "", err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

// Open returns a file handle for key.
func (b *Blobs) Open(_ context.Context, key string) (blobs.ReadSeekCloser, blobs.Info, error) {
	p, err := b.path(key)
	if err != nil {
		return nil, blobs.Info{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, blobs.Info{}, blobs.ErrNotFound
		}
		return nil, blobs.Info{}, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, blobs.Info{}, err
	}
	return f, blobs.Info{Key: key, Size: st.Size(), ModTime: st.ModTime()}, nil
}

// Delete removes key. Missing keys are ignored.
func (b *Blobs) Delete(_ context.Context, key string) error {
	p, err := b.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

// List returns all blobs under prefix.
func (b *Blobs) List(_ context.Context, prefix string) ([]blobs.Info, error) {
	base := b.root
	if prefix != "" {
		if err := ValidateKey(strings.TrimSuffix(prefix, "/")); err != nil && prefix != "" {
			// allow trailing slash
			trimmed := strings.TrimSuffix(prefix, "/")
			if trimmed == "" || ValidateKey(trimmed) != nil {
				return nil, err
			}
		}
		base = filepath.Join(b.root, filepath.FromSlash(strings.TrimSuffix(prefix, "/")))
	}
	var out []blobs.Info
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(b.root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, blobs.Info{
			Key:     filepath.ToSlash(rel),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

var _ blobs.Blobs = (*Blobs)(nil)
