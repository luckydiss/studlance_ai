package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/luckydiss/studlance_ai/internal/jobs"
	"github.com/luckydiss/studlance_ai/internal/store"
)

// blobResponse serves a stored blob with Range support. It implements the
// Visit…Response method of every binary GET endpoint added in PR 3 (admin
// draft/version resources, worker input download).
type blobResponse struct {
	body         io.ReadSeeker
	req          *http.Request
	contentType  string
	downloadName string // when set, sent as an attachment
}

func (resp blobResponse) serve(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", resp.contentType)
	if resp.downloadName != "" {
		writeAttachment(w, resp.downloadName)
	}
	http.ServeContent(w, resp.req, resp.downloadName, time.Time{}, resp.body)
	if c, ok := resp.body.(io.Closer); ok {
		_ = c.Close()
	}
	return nil
}

func (resp blobResponse) VisitAdminGetDraftFileResponse(w http.ResponseWriter) error {
	return resp.serve(w)
}

func (resp blobResponse) VisitAdminGetDraftPageResponse(w http.ResponseWriter) error {
	return resp.serve(w)
}

func (resp blobResponse) VisitAdminGetDraftThumbResponse(w http.ResponseWriter) error {
	return resp.serve(w)
}

func (resp blobResponse) VisitAdminGetVersionFileResponse(w http.ResponseWriter) error {
	return resp.serve(w)
}

func (resp blobResponse) VisitAdminGetVersionPageResponse(w http.ResponseWriter) error {
	return resp.serve(w)
}

func (resp blobResponse) VisitAdminGetVersionThumbResponse(w http.ResponseWriter) error {
	return resp.serve(w)
}

func (resp blobResponse) VisitAdminGetVersionBundleResponse(w http.ResponseWriter) error {
	return resp.serve(w)
}

func (resp blobResponse) VisitWorkerGetInputResponse(w http.ResponseWriter) error {
	return resp.serve(w)
}

// adminAuth checks the admin role, reporting whether access was denied and
// whether the request was at least authenticated (401 vs 403).
func (s *Server) adminAuth(ctx context.Context, r *http.Request) (denied, unauthenticated bool) {
	if _, ok := s.requireAdmin(ctx, r); ok {
		return false, false
	}
	return true, !s.authenticated(ctx, r)
}

// ---------- draft (admin) ----------

// AdminListDraftPages implements GET /api/admin/jobs/{id}/draft/documents/{document_id}/pages.
func (s *Server) AdminListDraftPages(ctx context.Context, request AdminListDraftPagesRequestObject) (AdminListDraftPagesResponseObject, error) {
	if denied, unauth := s.adminAuth(ctx, requestFrom(ctx)); denied {
		if unauth {
			return AdminListDraftPages401JSONResponse{errUnauthorized()}, nil
		}
		return AdminListDraftPages403JSONResponse{errForbidden()}, nil
	}
	doc, ok := s.snapshotDocument(ctx, string(request.Id), store.SnapshotDraft, 0, string(request.DocumentId))
	if !ok {
		return AdminListDraftPages404JSONResponse{errNotFound()}, nil
	}
	pages, err := s.store.PagesByDocument(ctx, doc.ID)
	if err != nil {
		return nil, err
	}
	base := fmt.Sprintf("/api/admin/jobs/%s/draft", request.Id)
	return AdminListDraftPages200JSONResponse(PageList{Pages: pageViewsBase(base, doc.ID, pages)}), nil
}

// AdminGetDraftPage implements GET /api/admin/jobs/{id}/draft/pages/{document_id}/{page}.
func (s *Server) AdminGetDraftPage(ctx context.Context, request AdminGetDraftPageRequestObject) (AdminGetDraftPageResponseObject, error) {
	if denied, unauth := s.adminAuth(ctx, requestFrom(ctx)); denied {
		if unauth {
			return AdminGetDraftPage401JSONResponse{errUnauthorized()}, nil
		}
		return AdminGetDraftPage403JSONResponse{errForbidden()}, nil
	}
	doc, ok := s.snapshotDocument(ctx, string(request.Id), store.SnapshotDraft, 0, string(request.DocumentId))
	if !ok {
		return AdminGetDraftPage404JSONResponse{errNotFound()}, nil
	}
	key := fmt.Sprintf("jobs/%s/draft/pages/%d/%d.png", request.Id, doc.Idx, parsePageParam(request.Page))
	rc, _, err := s.openBlob(ctx, key)
	if err != nil {
		return AdminGetDraftPage404JSONResponse{errNotFound()}, nil
	}
	return blobResponse{body: rc, req: requestFrom(ctx), contentType: "image/png"}, nil
}

// AdminGetDraftThumb implements GET /api/admin/jobs/{id}/draft/thumbs/{document_id}/{page}.
func (s *Server) AdminGetDraftThumb(ctx context.Context, request AdminGetDraftThumbRequestObject) (AdminGetDraftThumbResponseObject, error) {
	if denied, unauth := s.adminAuth(ctx, requestFrom(ctx)); denied {
		if unauth {
			return AdminGetDraftThumb401JSONResponse{errUnauthorized()}, nil
		}
		return AdminGetDraftThumb403JSONResponse{errForbidden()}, nil
	}
	doc, ok := s.snapshotDocument(ctx, string(request.Id), store.SnapshotDraft, 0, string(request.DocumentId))
	if !ok {
		return AdminGetDraftThumb404JSONResponse{errNotFound()}, nil
	}
	key := fmt.Sprintf("jobs/%s/draft/thumbs/%d/%d.png", request.Id, doc.Idx, parsePageParam(request.Page))
	rc, _, err := s.openBlob(ctx, key)
	if err != nil {
		return AdminGetDraftThumb404JSONResponse{errNotFound()}, nil
	}
	return blobResponse{body: rc, req: requestFrom(ctx), contentType: "image/png"}, nil
}

// AdminGetDraftFile implements GET /api/admin/jobs/{id}/draft/files/{path}.
func (s *Server) AdminGetDraftFile(ctx context.Context, request AdminGetDraftFileRequestObject) (AdminGetDraftFileResponseObject, error) {
	if denied, unauth := s.adminAuth(ctx, requestFrom(ctx)); denied {
		if unauth {
			return AdminGetDraftFile401JSONResponse{errUnauthorized()}, nil
		}
		return AdminGetDraftFile403JSONResponse{errForbidden()}, nil
	}
	path, err := jobs.NormalizePath(request.Path)
	if err != nil {
		return AdminGetDraftFile404JSONResponse{errNotFound()}, nil
	}
	if _, err := s.store.JobByID(ctx, string(request.Id)); err != nil {
		return AdminGetDraftFile404JSONResponse{errNotFound()}, nil
	}
	key := fmt.Sprintf("jobs/%s/draft/out/%s", request.Id, path)
	rc, _, err := s.openBlob(ctx, key)
	if err != nil {
		return AdminGetDraftFile404JSONResponse{errNotFound()}, nil
	}
	return blobResponse{body: rc, req: requestFrom(ctx), contentType: "application/octet-stream", downloadName: path}, nil
}

// ---------- versions (admin, no owner check, unfinished versions visible) ----------

// AdminListVersionPages implements GET /api/admin/jobs/{id}/versions/{v}/documents/{document_id}/pages.
func (s *Server) AdminListVersionPages(ctx context.Context, request AdminListVersionPagesRequestObject) (AdminListVersionPagesResponseObject, error) {
	if denied, unauth := s.adminAuth(ctx, requestFrom(ctx)); denied {
		if unauth {
			return AdminListVersionPages401JSONResponse{errUnauthorized()}, nil
		}
		return AdminListVersionPages403JSONResponse{errForbidden()}, nil
	}
	doc, ok := s.snapshotDocument(ctx, string(request.Id), store.SnapshotVersion, int64(request.V), string(request.DocumentId))
	if !ok {
		return AdminListVersionPages404JSONResponse{errNotFound()}, nil
	}
	pages, err := s.store.PagesByDocument(ctx, doc.ID)
	if err != nil {
		return nil, err
	}
	base := fmt.Sprintf("/api/admin/jobs/%s/versions/%d", request.Id, request.V)
	return AdminListVersionPages200JSONResponse(PageList{Pages: pageViewsBase(base, doc.ID, pages)}), nil
}

// AdminGetVersionPage implements GET /api/admin/jobs/{id}/versions/{v}/pages/{document_id}/{page}.
func (s *Server) AdminGetVersionPage(ctx context.Context, request AdminGetVersionPageRequestObject) (AdminGetVersionPageResponseObject, error) {
	if denied, unauth := s.adminAuth(ctx, requestFrom(ctx)); denied {
		if unauth {
			return AdminGetVersionPage401JSONResponse{errUnauthorized()}, nil
		}
		return AdminGetVersionPage403JSONResponse{errForbidden()}, nil
	}
	doc, ok := s.snapshotDocument(ctx, string(request.Id), store.SnapshotVersion, int64(request.V), string(request.DocumentId))
	if !ok {
		return AdminGetVersionPage404JSONResponse{errNotFound()}, nil
	}
	key := fmt.Sprintf("jobs/%s/v%d/pages/%d/%d.png", request.Id, request.V, doc.Idx, parsePageParam(request.Page))
	rc, _, err := s.openBlob(ctx, key)
	if err != nil {
		return AdminGetVersionPage404JSONResponse{errNotFound()}, nil
	}
	return blobResponse{body: rc, req: requestFrom(ctx), contentType: "image/png"}, nil
}

// AdminGetVersionThumb implements GET /api/admin/jobs/{id}/versions/{v}/thumbs/{document_id}/{page}.
func (s *Server) AdminGetVersionThumb(ctx context.Context, request AdminGetVersionThumbRequestObject) (AdminGetVersionThumbResponseObject, error) {
	if denied, unauth := s.adminAuth(ctx, requestFrom(ctx)); denied {
		if unauth {
			return AdminGetVersionThumb401JSONResponse{errUnauthorized()}, nil
		}
		return AdminGetVersionThumb403JSONResponse{errForbidden()}, nil
	}
	doc, ok := s.snapshotDocument(ctx, string(request.Id), store.SnapshotVersion, int64(request.V), string(request.DocumentId))
	if !ok {
		return AdminGetVersionThumb404JSONResponse{errNotFound()}, nil
	}
	key := fmt.Sprintf("jobs/%s/v%d/thumbs/%d/%d.png", request.Id, request.V, doc.Idx, parsePageParam(request.Page))
	rc, _, err := s.openBlob(ctx, key)
	if err != nil {
		return AdminGetVersionThumb404JSONResponse{errNotFound()}, nil
	}
	return blobResponse{body: rc, req: requestFrom(ctx), contentType: "image/png"}, nil
}

// AdminGetVersionFile implements GET /api/admin/jobs/{id}/versions/{v}/files/{path}.
func (s *Server) AdminGetVersionFile(ctx context.Context, request AdminGetVersionFileRequestObject) (AdminGetVersionFileResponseObject, error) {
	if denied, unauth := s.adminAuth(ctx, requestFrom(ctx)); denied {
		if unauth {
			return AdminGetVersionFile401JSONResponse{errUnauthorized()}, nil
		}
		return AdminGetVersionFile403JSONResponse{errForbidden()}, nil
	}
	path, err := jobs.NormalizePath(request.Path)
	if err != nil || request.V < 1 {
		return AdminGetVersionFile404JSONResponse{errNotFound()}, nil
	}
	if _, err := s.store.JobByID(ctx, string(request.Id)); err != nil {
		return AdminGetVersionFile404JSONResponse{errNotFound()}, nil
	}
	key := fmt.Sprintf("jobs/%s/v%d/out/%s", request.Id, request.V, path)
	rc, _, err := s.openBlob(ctx, key)
	if err != nil {
		return AdminGetVersionFile404JSONResponse{errNotFound()}, nil
	}
	return blobResponse{body: rc, req: requestFrom(ctx), contentType: "application/octet-stream", downloadName: path}, nil
}

// AdminGetVersionBundle implements GET /api/admin/jobs/{id}/versions/{v}/bundle.zip.
func (s *Server) AdminGetVersionBundle(ctx context.Context, request AdminGetVersionBundleRequestObject) (AdminGetVersionBundleResponseObject, error) {
	if denied, unauth := s.adminAuth(ctx, requestFrom(ctx)); denied {
		if unauth {
			return AdminGetVersionBundle401JSONResponse{errUnauthorized()}, nil
		}
		return AdminGetVersionBundle403JSONResponse{errForbidden()}, nil
	}
	j, err := s.store.JobByID(ctx, string(request.Id))
	if err != nil || request.V < 1 {
		return AdminGetVersionBundle404JSONResponse{errNotFound()}, nil
	}
	body, _, err := s.openBundle(ctx, j, int64(request.V))
	if err != nil {
		return AdminGetVersionBundle404JSONResponse{errNotFound()}, nil
	}
	return blobResponse{body: body, req: requestFrom(ctx), contentType: "application/zip", downloadName: "bundle.zip"}, nil
}

// ---------- helpers ----------

// snapshotDocument finds a document of the given snapshot without owner or
// release checks (admin view).
func (s *Server) snapshotDocument(ctx context.Context, jobID, snapshot string, version int64, documentID string) (store.Document, bool) {
	if _, err := s.store.JobByID(ctx, jobID); err != nil {
		return store.Document{}, false
	}
	doc, err := s.store.DocumentByID(ctx, documentID)
	if err != nil || doc.JobID != jobID || doc.Snapshot != snapshot || doc.Version != version {
		return store.Document{}, false
	}
	return doc, true
}
