package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luckydiss/studlance_ai/internal/blobs"
	"github.com/luckydiss/studlance_ai/internal/jobs"
	"github.com/luckydiss/studlance_ai/internal/store"
)

// parsedRevision is the JSON `data` field of a revision multipart request.
type parsedRevision struct {
	Comment string              `json:"comment"`
	Remarks []parsedRemarkInput `json:"remarks"`
}

type parsedRemarkInput struct {
	DocumentId string  `json:"document_id"`
	Page       int     `json:"page"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	W          float64 `json:"w"`
	H          float64 `json:"h"`
	Text       string  `json:"text"`
}

// revisionFile is one attached file from a revision multipart request.
type revisionFile struct {
	Filename string
	Content  io.Reader
}

// openBlob opens a blob or returns an error suitable for a 404 response.
func (s *Server) openBlob(ctx context.Context, key string) (blobs.ReadSeekCloser, blobs.Info, error) {
	rc, info, err := s.blobs.Open(ctx, key)
	if err != nil {
		return nil, blobs.Info{}, err
	}
	return rc, info, nil
}

// ---------- version file ----------

type fileResponse struct {
	body     io.ReadSeeker
	size     int64
	filename string
	req      *http.Request
}

func (resp fileResponse) VisitClientGetVersionFileResponse(w http.ResponseWriter) error {
	writeAttachment(w, resp.filename)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, resp.req, resp.filename, time.Time{}, resp.body)
	if c, ok := resp.body.(io.Closer); ok {
		_ = c.Close()
	}
	return nil
}

// ---------- page / thumb ----------

type imageResponse struct {
	body io.ReadSeeker
	size int64
	req  *http.Request
}

func (resp imageResponse) VisitClientGetVersionPageResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, resp.req, "page.png", time.Time{}, resp.body)
	if c, ok := resp.body.(io.Closer); ok {
		_ = c.Close()
	}
	return nil
}

type thumbResponse struct {
	body io.ReadSeeker
	size int64
	req  *http.Request
}

func (resp thumbResponse) VisitClientGetVersionThumbResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, resp.req, "thumb.png", time.Time{}, resp.body)
	if c, ok := resp.body.(io.Closer); ok {
		_ = c.Close()
	}
	return nil
}

// ---------- admin raw log ----------

type adminLogResponse struct {
	body io.ReadSeeker
	size int64
	req  *http.Request
}

func (resp adminLogResponse) VisitAdminGetRunLogResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, resp.req, "run.jsonl", time.Time{}, resp.body)
	if c, ok := resp.body.(io.Closer); ok {
		_ = c.Close()
	}
	return nil
}

// ---------- bundle ----------

type bundleResponse struct {
	data []byte
}

func (resp bundleResponse) VisitClientGetVersionBundleResponse(w http.ResponseWriter) error {
	writeAttachment(w, "bundle.zip")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Length", strconv.Itoa(len(resp.data)))
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(resp.data)
	return err
}

// buildBundle builds (and caches) a zip of all out/ files of a version.
// Entry names are UTF-8; the EFS flag is set by archive/zip automatically.
func (s *Server) buildBundle(ctx context.Context, j store.Job, version int64) ([]byte, error) {
	cacheKey := fmt.Sprintf("jobs/%s/v%d/bundle.zip", j.ID, version)
	if data, err := s.readBlob(ctx, cacheKey); err == nil {
		return data, nil
	}
	prefix := fmt.Sprintf("jobs/%s/v%d/out/", j.ID, version)
	infos, err := s.blobs.List(ctx, strings.TrimSuffix(prefix, "/"))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	found := false
	for _, info := range infos {
		if !strings.HasPrefix(info.Key, prefix) {
			continue
		}
		rel := strings.TrimPrefix(info.Key, prefix)
		if rel == "" || strings.HasSuffix(rel, "bundle.zip") {
			continue
		}
		rc, _, oErr := s.blobs.Open(ctx, info.Key)
		if oErr != nil {
			return nil, oErr
		}
		fw, cErr := zw.Create(rel)
		if cErr == nil {
			_, cErr = io.Copy(fw, rc)
		}
		_ = rc.Close()
		if cErr != nil {
			_ = zw.Close()
			return nil, cErr
		}
		found = true
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if !found {
		return nil, store.ErrNotFound
	}
	data := buf.Bytes()
	_, _, _ = s.blobs.Put(ctx, cacheKey, bytes.NewReader(data))
	return data, nil
}

func (s *Server) readBlob(ctx context.Context, key string) ([]byte, error) {
	rc, _, err := s.blobs.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

// writeAttachment sets a UTF-8 Content-Disposition attachment header.
func writeAttachment(w http.ResponseWriter, filename string) {
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+uriEncodePath(filename))
}

// uriEncodePath percent-encodes each path segment (including '/'-separated).
func uriEncodePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = urlPathEscape(s)
	}
	return strings.Join(parts, "/")
}

func urlPathEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

var _ = jobs.NormalizePath
