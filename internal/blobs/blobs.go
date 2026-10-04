// Package blobs defines content-addressable-ish file storage behind an
// interface. The MVP implementation is a folder on disk (blobs/fs).
package blobs

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrInvalidKey is returned when a key is not a clean relative path.
var ErrInvalidKey = errors.New("invalid blob key")

// ErrNotFound is returned when the key does not exist.
var ErrNotFound = errors.New("blob not found")

// Info describes a stored blob.
type Info struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// ReadSeekCloser is a readable, seekable, closeable blob handle.
type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

// Blobs stores opaque bytes under string keys (relative paths with '/').
type Blobs interface {
	// Put writes r under key and returns the number of bytes and their
	// sha256 (hex). Writes are atomic.
	Put(ctx context.Context, key string, r io.Reader) (size int64, sha256 string, err error)
	// Open returns a readable handle and its info.
	Open(ctx context.Context, key string) (ReadSeekCloser, Info, error)
	// Delete removes a blob. Missing keys are not an error.
	Delete(ctx context.Context, key string) error
	// List returns info for all blobs under prefix.
	List(ctx context.Context, prefix string) ([]Info, error)
}
