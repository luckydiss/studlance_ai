package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/luckydiss/studlance_ai/internal/auth"
	"github.com/luckydiss/studlance_ai/internal/jobs"
	"github.com/luckydiss/studlance_ai/internal/live"
	"github.com/luckydiss/studlance_ai/internal/store"
	"github.com/luckydiss/studlance_ai/internal/token"
)

// ClientListJobs implements GET /api/client/jobs.
func (s *Server) ClientListJobs(ctx context.Context, request ClientListJobsRequestObject) (ClientListJobsResponseObject, error) {
	su, ok := s.requireClient(ctx, requestFrom(ctx))
	if !ok {
		return ClientListJobs401JSONResponse{errUnauthorized()}, nil
	}
	rows, err := s.store.JobsByUser(ctx, su.User.ID)
	if err != nil {
		return nil, err
	}
	out := make([]JobSummary, 0, len(rows))
	for _, j := range rows {
		out = append(out, clientJobSummary(j))
	}
	return ClientListJobs200JSONResponse(ClientJobList{Jobs: out}), nil
}

// ClientCreateJob implements POST /api/client/jobs.
func (s *Server) ClientCreateJob(ctx context.Context, request ClientCreateJobRequestObject) (ClientCreateJobResponseObject, error) {
	su, ok := s.requireClient(ctx, requestFrom(ctx))
	if !ok {
		return ClientCreateJob401JSONResponse{errUnauthorized()}, nil
	}
	if request.Body == nil {
		return ClientCreateJob400JSONResponse{errBadRequest("Пустой запрос")}, nil
	}
	prompt := strings.TrimSpace(request.Body.Prompt)
	if len([]rune(prompt)) > 20000 {
		return ClientCreateJob400JSONResponse{errBadRequest("Запрос должен быть до 20000 символов")}, nil
	}
	now := s.now()
	j := store.Job{
		ID:        auth.NewID(),
		UserID:    su.User.ID,
		Title:     jobs.TitleFromPrompt(prompt),
		Prompt:    prompt,
		Status:    store.StatusUploading,
		State:     "{}",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.CreateJob(ctx, j); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, j.ID, "created", true, nil)
	s.publishJob(ctx, j)
	detail, err := s.clientJobDetail(ctx, j)
	if err != nil {
		return nil, err
	}
	return ClientCreateJob201JSONResponse(detail), nil
}

// ClientGetJob implements GET /api/client/jobs/{id}.
func (s *Server) ClientGetJob(ctx context.Context, request ClientGetJobRequestObject) (ClientGetJobResponseObject, error) {
	j, ok := s.clientJob(ctx, string(request.Id), requestFrom(ctx))
	if !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return ClientGetJob401JSONResponse{errUnauthorized()}, nil
		}
		return ClientGetJob404JSONResponse{errNotFound()}, nil
	}
	detail, err := s.clientJobDetail(ctx, j)
	if err != nil {
		return nil, err
	}
	return ClientGetJob200JSONResponse(detail), nil
}

// ClientUploadInput implements PUT /api/client/jobs/{id}/input?path=.
func (s *Server) ClientUploadInput(ctx context.Context, request ClientUploadInputRequestObject) (ClientUploadInputResponseObject, error) {
	j, ok := s.clientJob(ctx, string(request.Id), requestFrom(ctx))
	if !ok {
		return s.uploadMissingResult(ctx, requestFrom(ctx))
	}
	if j.Status != store.StatusUploading {
		return ClientUploadInput409JSONResponse{errConflict("Файлы можно загружать только до отправки заказа")}, nil
	}
	path, err := jobs.NormalizePath(string(request.Params.Path))
	if err != nil {
		return ClientUploadInput400JSONResponse{errBadRequest("Недопустимый путь файла")}, nil
	}

	// Total limit for the order: the new file replaces any existing one at the
	// same path, so the remaining allowance is (max - used + replaced size).
	used, err := s.store.SumInputBytes(ctx, j.ID)
	if err != nil {
		return nil, err
	}
	var replacedSize int64
	existing, existingErr := s.store.InputFileByPath(ctx, j.ID, path)
	if existingErr == nil {
		replacedSize = existing.Size
	}
	remaining := s.cfg.MaxUpload - (used - replacedSize)
	if remaining < 0 {
		remaining = 0
	}

	key := fmt.Sprintf("jobs/%s/input/%s", j.ID, path)
	body := http.MaxBytesReader(responseFrom(ctx), io.NopCloser(request.Body), remaining)
	size, sha, putErr := s.blobs.Put(ctx, key, body)
	if putErr != nil {
		var maxErr *http.MaxBytesError
		if errors.As(putErr, &maxErr) {
			return ClientUploadInput413JSONResponse{errTooLarge("Превышен лимит загрузки на заказ")}, nil
		}
		return nil, putErr
	}
	if used-replacedSize+size > s.cfg.MaxUpload {
		_ = s.blobs.Delete(ctx, key)
		return ClientUploadInput413JSONResponse{errTooLarge("Превышен лимит загрузки на заказ")}, nil
	}

	// Remove the previous record only after the new blobs are written.
	if existingErr == nil {
		if err := s.store.DeleteInputFileByPath(ctx, j.ID, path); err != nil {
			return nil, err
		}
		if existing.BlobKey != key {
			_ = s.blobs.Delete(ctx, existing.BlobKey)
		}
	}
	now := s.now()
	if err := s.store.CreateFile(ctx, store.File{
		ID: auth.NewID(), JobID: j.ID, Kind: store.FileInput, Version: 0,
		Path: path, BlobKey: key, Size: size, SHA256: sha, CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	s.publishJob(ctx, j)
	return ClientUploadInput200JSONResponse(UploadResult{Path: path, Size: int(size)}), nil
}

// ClientDeleteInput implements DELETE /api/client/jobs/{id}/input?path=.
func (s *Server) ClientDeleteInput(ctx context.Context, request ClientDeleteInputRequestObject) (ClientDeleteInputResponseObject, error) {
	j, ok := s.clientJob(ctx, string(request.Id), requestFrom(ctx))
	if !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return ClientDeleteInput401JSONResponse{errUnauthorized()}, nil
		}
		return ClientDeleteInput404JSONResponse{errNotFound()}, nil
	}
	if j.Status != store.StatusUploading {
		return ClientDeleteInput409JSONResponse{errConflict("Файлы можно удалять только до отправки заказа")}, nil
	}
	path, err := jobs.NormalizePath(string(request.Params.Path))
	if err != nil {
		return ClientDeleteInput404JSONResponse{errNotFound()}, nil
	}
	existing, fErr := s.store.InputFileByPath(ctx, j.ID, path)
	if fErr != nil {
		return ClientDeleteInput404JSONResponse{errNotFound()}, nil
	}
	_ = s.blobs.Delete(ctx, existing.BlobKey)
	if err := s.store.DeleteInputFileByPath(ctx, j.ID, path); err != nil {
		return nil, err
	}
	return ClientDeleteInput204Response{}, nil
}

// ClientSubmitJob implements POST /api/client/jobs/{id}/submit.
func (s *Server) ClientSubmitJob(ctx context.Context, request ClientSubmitJobRequestObject) (ClientSubmitJobResponseObject, error) {
	j, ok := s.clientJob(ctx, string(request.Id), requestFrom(ctx))
	if !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return ClientSubmitJob401JSONResponse{errUnauthorized()}, nil
		}
		return ClientSubmitJob404JSONResponse{errNotFound()}, nil
	}
	if j.Status != store.StatusUploading {
		return ClientSubmitJob409JSONResponse{errConflict("Заказ уже отправлен")}, nil
	}
	files, err := s.store.InputFiles(ctx, j.ID)
	if err != nil {
		return nil, err
	}
	hasFiles := len(files) > 0
	validPrompt := len([]rune(strings.TrimSpace(j.Prompt))) >= 20
	if !hasFiles && !validPrompt {
		return ClientSubmitJob409JSONResponse{errConflict("Нужен хотя бы один файл или запрос от 20 символов")}, nil
	}
	j.Status = store.StatusQueued
	j.Stage = store.StageDraft
	j.UpdatedAt = s.now()
	if err := s.store.UpdateJob(ctx, j); err != nil {
		return nil, err
	}
	var bytesTotal int64
	for _, f := range files {
		bytesTotal += f.Size
	}
	s.recordEvent(ctx, j.ID, "submitted", true, map[string]interface{}{"files": len(files), "bytes": bytesTotal})
	s.queue.Wake()
	s.publishJob(ctx, j)
	return s.submitResult(ctx, j)
}

// ClientAnswerJob implements POST /api/client/jobs/{id}/answer.
func (s *Server) ClientAnswerJob(ctx context.Context, request ClientAnswerJobRequestObject) (ClientAnswerJobResponseObject, error) {
	j, ok := s.clientJob(ctx, string(request.Id), requestFrom(ctx))
	if !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return ClientAnswerJob401JSONResponse{errUnauthorized()}, nil
		}
		return ClientAnswerJob404JSONResponse{errNotFound()}, nil
	}
	if request.Body == nil || strings.TrimSpace(request.Body.Text) == "" || len([]rune(request.Body.Text)) > 10000 {
		return ClientAnswerJob400JSONResponse{errBadRequest("Ответ должен быть от 1 до 10000 символов")}, nil
	}
	if j.Status != store.StatusNeedsInput {
		return ClientAnswerJob409JSONResponse{errConflict("Сейчас отвечать не требуется")}, nil
	}
	if err := s.applyAnswer(ctx, j, request.Body.Text, "client"); err != nil {
		return nil, err
	}
	updated, _ := s.store.JobByID(ctx, j.ID)
	s.publishJob(ctx, updated)
	detail, err := s.clientJobDetail(ctx, updated)
	if err != nil {
		return nil, err
	}
	return ClientAnswerJob200JSONResponse(detail), nil
}

// ClientCancelJob implements POST /api/client/jobs/{id}/cancel.
func (s *Server) ClientCancelJob(ctx context.Context, request ClientCancelJobRequestObject) (ClientCancelJobResponseObject, error) {
	j, ok := s.clientJob(ctx, string(request.Id), requestFrom(ctx))
	if !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return ClientCancelJob401JSONResponse{errUnauthorized()}, nil
		}
		return ClientCancelJob404JSONResponse{errNotFound()}, nil
	}
	if !jobs.CanCancel(j) {
		return ClientCancelJob409JSONResponse{errConflict("Заказ нельзя отменить")}, nil
	}
	if err := s.applyCancel(ctx, j); err != nil {
		return nil, err
	}
	updated, _ := s.store.JobByID(ctx, j.ID)
	s.publishJob(ctx, updated)
	detail, err := s.clientJobDetail(ctx, updated)
	if err != nil {
		return nil, err
	}
	return ClientCancelJob200JSONResponse(detail), nil
}

// ClientCreateRevision implements POST /api/client/jobs/{id}/revisions.
func (s *Server) ClientCreateRevision(ctx context.Context, request ClientCreateRevisionRequestObject) (ClientCreateRevisionResponseObject, error) {
	j, ok := s.clientJob(ctx, string(request.Id), requestFrom(ctx))
	if !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return ClientCreateRevision401JSONResponse{errUnauthorized()}, nil
		}
		return ClientCreateRevision404JSONResponse{errNotFound()}, nil
	}
	if !jobs.CanRevise(j) {
		return ClientCreateRevision409JSONResponse{errConflict("Доработка сейчас недоступна")}, nil
	}
	if request.Body == nil {
		return ClientCreateRevision400JSONResponse{errBadRequest("Пустая доработка")}, nil
	}
	version := j.CurrentVersion + 1
	revPrefix := fmt.Sprintf("revision-%d", version)

	// Files stream into blobs while their multipart parts are readable; if the
	// request is rejected afterwards, everything saved here is rolled back.
	type savedFile struct{ key, path string }
	var saved []savedFile
	rollback := func() {
		for _, sf := range saved {
			_ = s.blobs.Delete(ctx, sf.key)
			_ = s.store.DeleteInputFileByPath(ctx, j.ID, sf.path)
		}
	}
	saveFile := func(filename string, content io.Reader) error {
		path, pErr := jobs.NormalizePath(filename)
		if pErr != nil {
			return errInvalidRevisionFile
		}
		storedPath := fmt.Sprintf("input/%s/%s", revPrefix, path)
		key := fmt.Sprintf("jobs/%s/input/%s/%s", j.ID, revPrefix, path)
		limited := io.LimitReader(content, s.cfg.MaxUpload+1)
		size, sha, putErr := s.blobs.Put(ctx, key, limited)
		if putErr != nil {
			return putErr
		}
		if size > s.cfg.MaxUpload {
			_ = s.blobs.Delete(ctx, key)
			return errInvalidRevisionFile
		}
		if err := s.store.CreateFile(ctx, store.File{
			ID: auth.NewID(), JobID: j.ID, Kind: store.FileInput, Version: version,
			Path: storedPath, BlobKey: key, Size: size, SHA256: sha, CreatedAt: s.now(),
		}); err != nil {
			_ = s.blobs.Delete(ctx, key)
			return err
		}
		saved = append(saved, savedFile{key: key, path: storedPath})
		return nil
	}

	parsed, err := parseRevisionMultipart(request.Body, saveFile)
	if err != nil {
		rollback()
		return ClientCreateRevision400JSONResponse{errBadRequest("Неверные данные доработки")}, nil
	}
	comment := strings.TrimSpace(parsed.Comment)
	if comment == "" && len(parsed.Remarks) == 0 {
		rollback()
		return ClientCreateRevision400JSONResponse{errBadRequest("Нужен комментарий или хотя бы одно замечание")}, nil
	}

	rev := store.Revision{
		ID:        auth.NewID(),
		JobID:     j.ID,
		Version:   version,
		Comment:   comment,
		CreatedAt: s.now(),
	}
	remarks := make([]store.Remark, 0, len(parsed.Remarks))
	for i, r := range parsed.Remarks {
		if !validRemark(r) {
			rollback()
			return ClientCreateRevision400JSONResponse{errBadRequest("Неверное замечание")}, nil
		}
		doc, dErr := s.store.DocumentByID(ctx, r.DocumentId)
		if dErr != nil || doc.JobID != j.ID || doc.Snapshot != store.SnapshotVersion || doc.Version != j.CurrentVersion {
			rollback()
			return ClientCreateRevision400JSONResponse{errBadRequest("Замечание ссылается на неизвестный документ")}, nil
		}
		if int64(r.Page) < 1 || int64(r.Page) > doc.PageCount {
			rollback()
			return ClientCreateRevision400JSONResponse{errBadRequest("Неверный номер страницы замечания")}, nil
		}
		remarks = append(remarks, store.Remark{
			ID: auth.NewID(), RevisionID: rev.ID, Idx: int64(i + 1), DocumentID: r.DocumentId,
			Page: int64(r.Page), X: r.X, Y: r.Y, W: r.W, H: r.H, Text: strings.TrimSpace(r.Text),
		})
	}

	if err := s.store.CreateRevision(ctx, rev, remarks); err != nil {
		rollback()
		return nil, err
	}
	pending := version
	j.Stage = store.StageRevise
	j.Status = store.StatusQueued
	j.PendingRevision = &pending
	j.NeedsAttention = true
	j.UpdatedAt = s.now()
	if err := s.store.UpdateJob(ctx, j); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, j.ID, "revision_requested", true, map[string]interface{}{"version": version, "remarks": len(remarks)})
	s.queue.Wake()
	s.publishJob(ctx, j)
	detail, err := s.clientJobDetail(ctx, j)
	if err != nil {
		return nil, err
	}
	return ClientCreateRevision201JSONResponse(detail), nil
}

// ClientListVersionPages implements GET .../versions/{v}/documents/{document_id}/pages.
func (s *Server) ClientListVersionPages(ctx context.Context, request ClientListVersionPagesRequestObject) (ClientListVersionPagesResponseObject, error) {
	j, ok := s.clientJob(ctx, string(request.Id), requestFrom(ctx))
	if !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return ClientListVersionPages401JSONResponse{errUnauthorized()}, nil
		}
		return ClientListVersionPages404JSONResponse{errNotFound()}, nil
	}
	doc, ok := s.versionDocument(ctx, j, int64(request.V), string(request.DocumentId))
	if !ok {
		return ClientListVersionPages404JSONResponse{errNotFound()}, nil
	}
	pages, err := s.store.PagesByDocument(ctx, doc.ID)
	if err != nil {
		return nil, err
	}
	base := fmt.Sprintf("/api/client/jobs/%s/versions/%d", j.ID, request.V)
	return ClientListVersionPages200JSONResponse(PageList{Pages: pageViewsBase(base, doc.ID, pages)}), nil
}

// ClientGetVersionPage implements GET .../versions/{v}/pages/{document_id}/{page}.png.
func (s *Server) ClientGetVersionPage(ctx context.Context, request ClientGetVersionPageRequestObject) (ClientGetVersionPageResponseObject, error) {
	j, ok := s.clientJob(ctx, string(request.Id), requestFrom(ctx))
	if !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return ClientGetVersionPage401JSONResponse{errUnauthorized()}, nil
		}
		return ClientGetVersionPage404JSONResponse{errNotFound()}, nil
	}
	doc, ok := s.versionDocument(ctx, j, int64(request.V), string(request.DocumentId))
	if !ok {
		return ClientGetVersionPage404JSONResponse{errNotFound()}, nil
	}
	page := parsePageParam(request.Page)
	key := fmt.Sprintf("jobs/%s/v%d/pages/%d/%d.png", j.ID, int64(request.V), doc.Idx, page)
	rc, info, err := s.openBlob(ctx, key)
	if err != nil {
		return ClientGetVersionPage404JSONResponse{errNotFound()}, nil
	}
	return imageResponse{body: rc, size: info.Size, req: requestFrom(ctx)}, nil
}

// ClientGetVersionThumb implements GET .../versions/{v}/thumbs/{document_id}/{page}.png.
func (s *Server) ClientGetVersionThumb(ctx context.Context, request ClientGetVersionThumbRequestObject) (ClientGetVersionThumbResponseObject, error) {
	j, ok := s.clientJob(ctx, string(request.Id), requestFrom(ctx))
	if !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return ClientGetVersionThumb401JSONResponse{errUnauthorized()}, nil
		}
		return ClientGetVersionThumb404JSONResponse{errNotFound()}, nil
	}
	doc, ok := s.versionDocument(ctx, j, int64(request.V), string(request.DocumentId))
	if !ok {
		return ClientGetVersionThumb404JSONResponse{errNotFound()}, nil
	}
	page := parsePageParam(request.Page)
	key := fmt.Sprintf("jobs/%s/v%d/thumbs/%d/%d.png", j.ID, int64(request.V), doc.Idx, page)
	rc, info, err := s.openBlob(ctx, key)
	if err != nil {
		return ClientGetVersionThumb404JSONResponse{errNotFound()}, nil
	}
	return thumbResponse{body: rc, size: info.Size, req: requestFrom(ctx)}, nil
}

// ClientGetVersionFile implements GET .../versions/{v}/files/{path}.
func (s *Server) ClientGetVersionFile(ctx context.Context, request ClientGetVersionFileRequestObject) (ClientGetVersionFileResponseObject, error) {
	j, ok := s.clientJob(ctx, string(request.Id), requestFrom(ctx))
	if !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return ClientGetVersionFile401JSONResponse{errUnauthorized()}, nil
		}
		return ClientGetVersionFile404JSONResponse{errNotFound()}, nil
	}
	path, pErr := jobs.NormalizePath(request.Path)
	if pErr != nil {
		return ClientGetVersionFile404JSONResponse{errNotFound()}, nil
	}
	if !versionAvailable(j, int64(request.V)) {
		return ClientGetVersionFile404JSONResponse{errNotFound()}, nil
	}
	key := fmt.Sprintf("jobs/%s/v%d/out/%s", j.ID, int64(request.V), path)
	rc, info, err := s.openBlob(ctx, key)
	if err != nil {
		return ClientGetVersionFile404JSONResponse{errNotFound()}, nil
	}
	return fileResponse{body: rc, size: info.Size, filename: path, req: requestFrom(ctx)}, nil
}

// ClientGetVersionBundle implements GET .../versions/{v}/bundle.zip.
func (s *Server) ClientGetVersionBundle(ctx context.Context, request ClientGetVersionBundleRequestObject) (ClientGetVersionBundleResponseObject, error) {
	j, ok := s.clientJob(ctx, string(request.Id), requestFrom(ctx))
	if !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return ClientGetVersionBundle401JSONResponse{errUnauthorized()}, nil
		}
		return ClientGetVersionBundle404JSONResponse{errNotFound()}, nil
	}
	if !versionAvailable(j, int64(request.V)) {
		return ClientGetVersionBundle404JSONResponse{errNotFound()}, nil
	}
	body, size, err := s.openBundle(ctx, j, int64(request.V))
	if err != nil {
		return ClientGetVersionBundle404JSONResponse{errNotFound()}, nil
	}
	return bundleResponse{body: body, size: size, req: requestFrom(ctx)}, nil
}

// ---------- shared client helpers ----------

func (s *Server) submitResult(ctx context.Context, j store.Job) (ClientSubmitJobResponseObject, error) {
	detail, err := s.clientJobDetail(ctx, j)
	if err != nil {
		return nil, err
	}
	return ClientSubmitJob200JSONResponse(detail), nil
}

func (s *Server) uploadMissingResult(ctx context.Context, r *http.Request) (ClientUploadInputResponseObject, error) {
	if !s.authenticated(ctx, r) {
		return ClientUploadInput401JSONResponse{errUnauthorized()}, nil
	}
	return ClientUploadInput404JSONResponse{errNotFound()}, nil
}

func (s *Server) authenticated(ctx context.Context, r *http.Request) bool {
	_, ok := s.currentUser(ctx, r)
	return ok
}

// clientJob loads a job owned by the requesting client, or returns ok=false.
func (s *Server) clientJob(ctx context.Context, id string, r *http.Request) (store.Job, bool) {
	su, ok := s.requireClient(ctx, r)
	if !ok {
		return store.Job{}, false
	}
	j, err := s.store.JobByIDForUser(ctx, id, su.User.ID)
	if err != nil {
		return store.Job{}, false
	}
	return j, true
}

func (s *Server) applyAnswer(ctx context.Context, j store.Job, text, by string) error {
	state := mergeState(j.State, map[string]interface{}{"pending_answer": text})
	j.Status = store.StatusQueued
	j.Question = nil
	j.UpdatedAt = s.now()
	if err := s.store.UpdateJob(ctx, j); err != nil {
		return err
	}
	if err := s.store.SetJobState(ctx, j.ID, state, s.now()); err != nil {
		return err
	}
	s.recordEvent(ctx, j.ID, "answered", true, map[string]interface{}{"by": by})
	s.queue.Wake()
	return nil
}

func (s *Server) applyCancel(ctx context.Context, j store.Job) error {
	if j.Status == store.StatusRunning {
		j.CancelRequested = true
		j.UpdatedAt = s.now()
		if err := s.store.UpdateJob(ctx, j); err != nil {
			return err
		}
		s.recordEvent(ctx, j.ID, "cancel_requested", true, nil)
		return nil
	}
	j.Status = store.StatusCanceled
	j.UpdatedAt = s.now()
	now := s.now()
	j.FinishedAt = &now
	if err := s.store.UpdateJob(ctx, j); err != nil {
		return err
	}
	s.recordEvent(ctx, j.ID, "canceled", true, nil)
	return nil
}

func (s *Server) recordEvent(ctx context.Context, jobID, kind string, visible bool, data map[string]interface{}) {
	raw := "{}"
	if data != nil {
		if b, err := json.Marshal(data); err == nil {
			raw = string(b)
		}
	}
	_ = s.store.CreateEvent(ctx, store.Event{
		ID: auth.NewID(), JobID: jobID, Ts: s.now(), Kind: kind,
		VisibleToClient: visible, Data: raw,
	})
}

// publishJob sends a "job" SSE event; the payload is filled by the stream.
func (s *Server) publishJob(ctx context.Context, j store.Job) {
	s.hub.Publish(j.ID, live.Event{Name: "job"})
}

// parsePageParam parses a page path parameter that may carry a ".png" suffix
// (the documented URLs end in .png; ServeMux cannot bind a literal suffix).
func parsePageParam(v PageNumber) int64 {
	s := strings.TrimSuffix(string(v), ".png")
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func mergeState(existing string, updates map[string]interface{}) string {
	m := map[string]interface{}{}
	if existing != "" {
		_ = json.Unmarshal([]byte(existing), &m)
	}
	for k, v := range updates {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return existing
	}
	return string(b)
}

// versionAvailable reports whether a version has been released to the client:
// only 1 <= v <= current_version (04-api.md).
func versionAvailable(j store.Job, version int64) bool {
	return version >= 1 && version <= j.CurrentVersion
}

// versionDocument returns the document of a version by id, or ok=false.
func (s *Server) versionDocument(ctx context.Context, j store.Job, version int64, documentID string) (store.Document, bool) {
	if !versionAvailable(j, version) {
		return store.Document{}, false
	}
	doc, err := s.store.DocumentByID(ctx, documentID)
	if err != nil || doc.JobID != j.ID || doc.Version != version || doc.Snapshot != store.SnapshotVersion {
		return store.Document{}, false
	}
	return doc, true
}

// validRemark validates a remark's coordinates and text (04-api.md revisions).
func validRemark(r parsedRemarkInput) bool {
	if r.DocumentId == "" {
		return false
	}
	if r.X < 0 || r.Y < 0 || r.W <= 0 || r.H <= 0 {
		return false
	}
	if r.X > 1 || r.Y > 1 || r.W > 1 || r.H > 1 {
		return false
	}
	if r.X+r.W > 1.000001 || r.Y+r.H > 1.000001 {
		return false
	}
	text := strings.TrimSpace(r.Text)
	if text == "" || len([]rune(text)) > 2000 {
		return false
	}
	return true
}

// parseRevisionMultipart streams the multipart body: the `data` field is
// parsed as JSON, and each file is handed to saveFile while its part is still
// readable (a part's content is gone once the iterator advances past it).
func parseRevisionMultipart(r *multipart.Reader, saveFile func(filename string, content io.Reader) error) (parsedRevision, error) {
	data := parsedRevision{}
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return parsedRevision{}, err
		}
		name := part.FormName()
		if name == "data" {
			raw, rErr := io.ReadAll(io.LimitReader(part, 1<<20))
			if rErr != nil {
				return parsedRevision{}, rErr
			}
			if uErr := json.Unmarshal(raw, &data); uErr != nil {
				return parsedRevision{}, uErr
			}
			continue
		}
		if name == "files" || name == "files[]" {
			filename := partFileName(part)
			if filename == "" {
				return parsedRevision{}, errors.New("empty filename")
			}
			if sErr := saveFile(filename, part); sErr != nil {
				return parsedRevision{}, sErr
			}
		}
	}
	return data, nil
}

// partFileName returns the full relative path from the Content-Disposition
// filename parameter. part.FileName() only returns the base name, dropping
// directories, so we parse the header ourselves (05-worker, 02-data paths).
func partFileName(part *multipart.Part) string {
	cd := part.Header.Get("Content-Disposition")
	if cd == "" {
		return part.FileName()
	}
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		return part.FileName()
	}
	return params["filename"]
}

var (
	_ = strconv.Itoa
	_ = token.Hash
	_ = time.Now
)

// errInvalidRevisionFile marks a rejected attachment (bad name or too large).
var errInvalidRevisionFile = errors.New("invalid revision file")
