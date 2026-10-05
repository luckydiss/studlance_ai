package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/luckydiss/studlance_ai/internal/auth"
	"github.com/luckydiss/studlance_ai/internal/jobs"
	"github.com/luckydiss/studlance_ai/internal/queue"
	"github.com/luckydiss/studlance_ai/internal/store"
	"github.com/luckydiss/studlance_ai/internal/token"
)

// workerErr classifies a queue error into HTTP status, code and message.
// ok=false means an internal error (logged, 500 by the strict wrapper).
func workerErr(err error) (status int, code, msg string, ok bool) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "not_found", "Не найдено", true
	case errors.Is(err, queue.ErrStaleLease):
		return http.StatusConflict, "stale_lease", "Устаревший lease: прекратите работу над заказом", true
	}
	var ce *queue.ConflictError
	if errors.As(err, &ce) {
		return http.StatusConflict, "conflict", ce.Message, true
	}
	return 0, "", "", false
}

func workerErrBody(err error) ErrorResponse {
	_, code, msg, _ := workerErr(err)
	return ErrorResponse{Error: ErrorBody{Code: code, Message: msg}}
}

// requireWorker authenticates the worker by its Bearer token and updates
// last_seen_at on every worker request (04-api.md).
func (s *Server) requireWorker(ctx context.Context, r *http.Request) (store.Worker, bool) {
	if r == nil {
		return store.Worker{}, false
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return store.Worker{}, false
	}
	w, err := s.store.WorkerByTokenHash(ctx, token.Hash(strings.TrimPrefix(h, "Bearer ")))
	if err != nil {
		return store.Worker{}, false
	}
	_ = s.store.TouchWorker(ctx, w.ID, s.now())
	return w, true
}

// WorkerRegister implements POST /api/worker/register.
func (s *Server) WorkerRegister(ctx context.Context, request WorkerRegisterRequestObject) (WorkerRegisterResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerRegister401JSONResponse{errUnauthorized()}, nil
	}
	if request.Body == nil {
		return nil, errBadBody()
	}
	if request.Body.Capabilities == nil {
		request.Body.Capabilities = []string{}
	}
	if request.Body.Info == nil {
		request.Body.Info = map[string]interface{}{}
	}
	if err := s.queue.RegisterWorker(ctx, w.ID, request.Body.Capabilities, request.Body.Info); err != nil {
		return nil, err
	}
	return WorkerRegister200JSONResponse(WorkerRegisterResponse{WorkerId: w.ID}), nil
}

// WorkerClaim implements POST /api/worker/claim (long-poll).
func (s *Server) WorkerClaim(ctx context.Context, request WorkerClaimRequestObject) (WorkerClaimResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerClaim401JSONResponse{errUnauthorized()}, nil
	}
	asn, err := s.queue.ClaimWait(ctx, w.ID, w.Name)
	if errors.Is(err, queue.ErrNoJob) {
		return WorkerClaim204Response{}, nil
	}
	if err != nil {
		return nil, err
	}
	return WorkerClaim200JSONResponse(assignmentView(asn)), nil
}

func assignmentView(a *queue.Assignment) Assignment {
	out := Assignment{
		Action:         AssignmentAction(a.Action),
		Answer:         a.Answer,
		Attempt:        int(a.Attempt),
		Epoch:          int(a.Epoch),
		JobId:          a.JobID,
		LeaseExpiresAt: a.LeaseExpiresAt,
		Prompt:         a.Prompt,
		Stage:          AssignmentStage(a.Stage),
		Version:        int(a.Version),
	}
	if a.State != nil {
		out.State = &a.State
	}
	if a.Revision != nil {
		rev := AssignmentRevision{
			Version: int(a.Revision.Version),
			Comment: a.Revision.Comment,
			Remarks: []AssignmentRemark{},
			Files:   []RevisionFile{},
		}
		for _, r := range a.Revision.Remarks {
			rev.Remarks = append(rev.Remarks, AssignmentRemark{
				Idx: int(r.Idx), DocumentTitle: r.DocumentTitle, FilePath: r.FilePath,
				Page: int(r.Page), X: float32(r.X), Y: float32(r.Y), W: float32(r.W), H: float32(r.H),
				Text: r.Text,
			})
		}
		for _, f := range a.Revision.Files {
			rev.Files = append(rev.Files, RevisionFile{Path: f})
		}
		out.Revision = &rev
	}
	return out
}

// WorkerHeartbeat implements POST /api/worker/jobs/{id}/heartbeat.
func (s *Server) WorkerHeartbeat(ctx context.Context, request WorkerHeartbeatRequestObject) (WorkerHeartbeatResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerHeartbeat401JSONResponse{errUnauthorized()}, nil
	}
	if request.Body == nil {
		return WorkerHeartbeat400JSONResponse{errBadRequest("Пустое тело")}, nil
	}
	expires, cancel, err := s.queue.Heartbeat(ctx, string(request.Id), w.ID, int64(request.Body.Epoch))
	if err != nil {
		return workerHeartbeatErr(err)
	}
	return WorkerHeartbeat200JSONResponse(HeartbeatResponse{LeaseExpiresAt: expires, Cancel: cancel}), nil
}

func workerHeartbeatErr(err error) (WorkerHeartbeatResponseObject, error) {
	status, _, _, ok := workerErr(err)
	if !ok {
		return nil, err
	}
	if status == http.StatusNotFound {
		return WorkerHeartbeat404JSONResponse{errNotFound()}, nil
	}
	return WorkerHeartbeat409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
}

// WorkerListInput implements GET /api/worker/jobs/{id}/input.
func (s *Server) WorkerListInput(ctx context.Context, request WorkerListInputRequestObject) (WorkerListInputResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerListInput401JSONResponse{errUnauthorized()}, nil
	}
	if err := s.queue.CheckLease(ctx, string(request.Id), w.ID, int64(request.Params.Epoch)); err != nil {
		status, _, _, ok := workerErr(err)
		if !ok {
			return nil, err
		}
		if status == http.StatusNotFound {
			return WorkerListInput404JSONResponse{errNotFound()}, nil
		}
		return WorkerListInput409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
	}
	files, err := s.store.InputFiles(ctx, string(request.Id))
	if err != nil {
		return nil, err
	}
	out := WorkerInputFileList{Files: []WorkerInputFile{}}
	for _, f := range files {
		out.Files = append(out.Files, WorkerInputFile{
			Path:     strings.TrimPrefix(f.Path, "input/"),
			Size:     int(f.Size),
			Sha256:   f.SHA256,
			Revision: int(f.Revision),
		})
	}
	return WorkerListInput200JSONResponse(out), nil
}

// WorkerGetInput implements GET /api/worker/jobs/{id}/input/{path}.
func (s *Server) WorkerGetInput(ctx context.Context, request WorkerGetInputRequestObject) (WorkerGetInputResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerGetInput401JSONResponse{errUnauthorized()}, nil
	}
	jobID := string(request.Id)
	if err := s.queue.CheckLease(ctx, jobID, w.ID, int64(request.Params.Epoch)); err != nil {
		return workerGetInputErr(err)
	}
	path, err := jobs.NormalizePath(request.Path)
	if err != nil {
		return WorkerGetInput404JSONResponse{errNotFound()}, nil
	}
	// Initial uploads are stored without a prefix, revision files with an
	// "input/" prefix (PR 2); accept both spellings.
	f, err := s.store.InputFileByPath(ctx, jobID, path)
	if err != nil {
		f, err = s.store.InputFileByPath(ctx, jobID, "input/"+path)
	}
	if err != nil {
		return WorkerGetInput404JSONResponse{errNotFound()}, nil
	}
	rc, _, err := s.blobs.Open(ctx, f.BlobKey)
	if err != nil {
		return WorkerGetInput404JSONResponse{errNotFound()}, nil
	}
	return blobResponse{body: rc, req: requestFrom(ctx), contentType: "application/octet-stream", downloadName: path}, nil
}

func workerGetInputErr(err error) (WorkerGetInputResponseObject, error) {
	status, _, _, ok := workerErr(err)
	if !ok {
		return nil, err
	}
	if status == http.StatusNotFound {
		return WorkerGetInput404JSONResponse{errNotFound()}, nil
	}
	return WorkerGetInput409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
}

// worker input downloads use blobResponse (VisitWorkerGetInputResponse).

// WorkerCreateRun implements POST /api/worker/jobs/{id}/runs.
func (s *Server) WorkerCreateRun(ctx context.Context, request WorkerCreateRunRequestObject) (WorkerCreateRunResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerCreateRun401JSONResponse{errUnauthorized()}, nil
	}
	if request.Body == nil {
		return WorkerCreateRun400JSONResponse{errBadRequest("Пустое тело")}, nil
	}
	b := request.Body
	agent := string(b.Agent)
	if agent != "codex" && agent != "claude" {
		return WorkerCreateRun400JSONResponse{errBadRequest("Неверный агент")}, nil
	}
	stage := string(b.Stage)
	if stage != store.StageDraft && stage != store.StageVerify && stage != store.StageRevise {
		return WorkerCreateRun400JSONResponse{errBadRequest("Неверный этап")}, nil
	}
	run := store.AgentRun{
		Version:   int64(b.Version),
		Agent:     agent,
		Stage:     stage,
		Attempt:   int64(b.Attempt),
		SessionID: b.SessionId,
		StartedAt: s.now(),
	}
	runID, err := s.queue.CreateRun(ctx, string(request.Id), w.ID, int64(b.Epoch), run)
	if err != nil {
		status, _, _, ok := workerErr(err)
		if !ok {
			return nil, err
		}
		if status == http.StatusNotFound {
			return WorkerCreateRun404JSONResponse{errNotFound()}, nil
		}
		return WorkerCreateRun409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
	}
	return WorkerCreateRun201JSONResponse(CreateRunResponse{RunId: runID}), nil
}

// WorkerPatchRun implements PATCH /api/worker/jobs/{id}/runs/{run_id}.
func (s *Server) WorkerPatchRun(ctx context.Context, request WorkerPatchRunRequestObject) (WorkerPatchRunResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerPatchRun401JSONResponse{errUnauthorized()}, nil
	}
	if request.Body == nil {
		return WorkerPatchRun400JSONResponse{errBadRequest("Пустое тело")}, nil
	}
	b := request.Body
	p := store.AgentRunPatch{
		SessionID:    b.SessionId,
		FinishedAt:   b.FinishedAt,
		InputTokens:  int64Ptr(b.InputTokens),
		OutputTokens: int64Ptr(b.OutputTokens),
		Error:        b.Error,
	}
	if b.ExitCode != nil {
		v := int64(*b.ExitCode)
		p.ExitCode = &v
	}
	if b.Outcome != nil {
		outcome := string(*b.Outcome)
		switch outcome {
		case queue.OutcomeOK, "question", queue.OutcomeFailed, queue.OutcomeCanceled, queue.OutcomeTimeout:
		default:
			return WorkerPatchRun400JSONResponse{errBadRequest("Неверный исход")}, nil
		}
		p.Outcome = &outcome
	}
	if b.CostUsd != nil {
		v := float64(*b.CostUsd)
		p.CostUSD = &v
	}
	err := s.queue.PatchRun(ctx, string(request.Id), request.RunId, w.ID, int64(b.Epoch), p)
	if err != nil {
		status, _, _, ok := workerErr(err)
		if !ok {
			return nil, err
		}
		if status == http.StatusNotFound {
			return WorkerPatchRun404JSONResponse{errNotFound()}, nil
		}
		return WorkerPatchRun409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
	}
	return WorkerPatchRun204Response{}, nil
}

// WorkerAppendSteps implements POST /api/worker/jobs/{id}/runs/{run_id}/steps.
func (s *Server) WorkerAppendSteps(ctx context.Context, request WorkerAppendStepsRequestObject) (WorkerAppendStepsResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerAppendSteps401JSONResponse{errUnauthorized()}, nil
	}
	if request.Body == nil {
		return WorkerAppendSteps400JSONResponse{errBadRequest("Пустое тело")}, nil
	}
	steps := make([]store.TraceStep, 0, len(request.Body.Steps))
	for _, st := range request.Body.Steps {
		payload := "{}"
		if st.Payload != nil {
			if raw, err := jsonString(*st.Payload); err == nil {
				payload = raw
			}
		}
		summary := st.Summary
		if len([]rune(summary)) > 300 {
			summary = string([]rune(summary)[:300])
		}
		steps = append(steps, store.TraceStep{
			Seq: int64(st.Seq), Ts: st.Ts, Type: st.Type, Summary: summary, Payload: payload,
		})
	}
	_, err := s.queue.AppendSteps(ctx, string(request.Id), request.RunId, w.ID, int64(request.Body.Epoch), steps)
	if err != nil {
		status, _, _, ok := workerErr(err)
		if !ok {
			return nil, err
		}
		if status == http.StatusNotFound {
			return WorkerAppendSteps404JSONResponse{errNotFound()}, nil
		}
		return WorkerAppendSteps409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
	}
	return WorkerAppendSteps204Response{}, nil
}

// WorkerPutRunLog implements PUT /api/worker/jobs/{id}/runs/{run_id}/log.
// The body streams into the blob store without in-memory buffering.
func (s *Server) WorkerPutRunLog(ctx context.Context, request WorkerPutRunLogRequestObject) (WorkerPutRunLogResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerPutRunLog401JSONResponse{errUnauthorized()}, nil
	}
	jobID := string(request.Id)
	if err := s.queue.CheckLease(ctx, jobID, w.ID, int64(request.Params.Epoch)); err != nil {
		return workerPutRunLogErr(err)
	}
	key := fmt.Sprintf("jobs/%s/logs/%s.jsonl", jobID, request.RunId)
	if _, _, err := s.blobs.Put(ctx, key, request.Body); err != nil {
		return nil, err
	}
	if err := s.queue.SaveRunLogKey(ctx, jobID, request.RunId, w.ID, int64(request.Params.Epoch), key); err != nil {
		_ = s.blobs.Delete(ctx, key)
		return workerPutRunLogErr(err)
	}
	return WorkerPutRunLog204Response{}, nil
}

func workerPutRunLogErr(err error) (WorkerPutRunLogResponseObject, error) {
	status, _, _, ok := workerErr(err)
	if !ok {
		return nil, err
	}
	if status == http.StatusNotFound {
		return WorkerPutRunLog404JSONResponse{errNotFound()}, nil
	}
	return WorkerPutRunLog409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
}

// WorkerSetState implements POST /api/worker/jobs/{id}/state (JSON-merge).
func (s *Server) WorkerSetState(ctx context.Context, request WorkerSetStateRequestObject) (WorkerSetStateResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerSetState401JSONResponse{errUnauthorized()}, nil
	}
	if request.Body == nil {
		return WorkerSetState400JSONResponse{errBadRequest("Пустое тело")}, nil
	}
	err := s.queue.MergeState(ctx, string(request.Id), w.ID, int64(request.Body.Epoch), request.Body.State)
	if err != nil {
		status, _, _, ok := workerErr(err)
		if !ok {
			return nil, err
		}
		if status == http.StatusNotFound {
			return WorkerSetState404JSONResponse{errNotFound()}, nil
		}
		return WorkerSetState409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
	}
	return WorkerSetState204Response{}, nil
}

// ---------- snapshots ----------

// maxSnapshotFile caps one snapshot file (pages, thumbs, crops are small).
const maxSnapshotFile = 256 << 20 // 256 MiB

// WorkerPutSnapshotFile implements PUT /api/worker/jobs/{id}/snapshot/{snapshot}/files?path=.
// Paths under preview/ are stored in the snapshot's preview/ directory
// (converted page previews); everything else belongs to out/.
func (s *Server) WorkerPutSnapshotFile(ctx context.Context, request WorkerPutSnapshotFileRequestObject) (WorkerPutSnapshotFileResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerPutSnapshotFile401JSONResponse{errUnauthorized()}, nil
	}
	snap, err := queue.ParseSnapshot(request.Snapshot)
	if err != nil {
		return WorkerPutSnapshotFile400JSONResponse{errBadRequest("Неверный снимок")}, nil
	}
	path, err := jobs.NormalizePath(request.Params.Path)
	if err != nil {
		return WorkerPutSnapshotFile400JSONResponse{errBadRequest("Недопустимый путь файла")}, nil
	}
	jobID := string(request.Id)
	if err := s.queue.CheckSnapshot(ctx, jobID, w.ID, int64(request.Params.Epoch), snap); err != nil {
		return workerPutSnapshotFileErr(err)
	}
	sub := "out/"
	if strings.HasPrefix(path, "preview/") {
		sub = ""
	}
	key := fmt.Sprintf("jobs/%s/%s/%s%s", jobID, snap.String(), sub, path)
	body := http.MaxBytesReader(responseFrom(ctx), io.NopCloser(request.Body), maxSnapshotFile)
	size, sha, err := s.blobs.Put(ctx, key, body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return WorkerPutSnapshotFile400JSONResponse{errBadRequest("Файл слишком большой")}, nil
		}
		return nil, err
	}
	if err := s.queue.RecordSnapshotFile(ctx, jobID, w.ID, int64(request.Params.Epoch), snap, path, key, size, sha); err != nil {
		_ = s.blobs.Delete(ctx, key)
		return workerPutSnapshotFileErr(err)
	}
	return WorkerPutSnapshotFile204Response{}, nil
}

func workerPutSnapshotFileErr(err error) (WorkerPutSnapshotFileResponseObject, error) {
	status, _, _, ok := workerErr(err)
	if !ok {
		return nil, err
	}
	if status == http.StatusNotFound {
		return WorkerPutSnapshotFile404JSONResponse{errNotFound()}, nil
	}
	return WorkerPutSnapshotFile409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
}

// WorkerPutSnapshotPage implements PUT .../snapshot/{snapshot}/pages/{document_idx}/{page}.
func (s *Server) WorkerPutSnapshotPage(ctx context.Context, request WorkerPutSnapshotPageRequestObject) (WorkerPutSnapshotPageResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerPutSnapshotPage401JSONResponse{errUnauthorized()}, nil
	}
	snap, err := queue.ParseSnapshot(request.Snapshot)
	if err != nil {
		return WorkerPutSnapshotPage400JSONResponse{errBadRequest("Неверный снимок")}, nil
	}
	if request.DocumentIdx < 0 || request.Page < 1 {
		return WorkerPutSnapshotPage400JSONResponse{errBadRequest("Неверная страница")}, nil
	}
	jobID := string(request.Id)
	if err := s.queue.CheckSnapshot(ctx, jobID, w.ID, int64(request.Params.Epoch), snap); err != nil {
		status, _, _, ok := workerErr(err)
		if !ok {
			return nil, err
		}
		if status == http.StatusNotFound {
			return WorkerPutSnapshotPage404JSONResponse{errNotFound()}, nil
		}
		return WorkerPutSnapshotPage409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
	}
	key := fmt.Sprintf("jobs/%s/%s/pages/%d/%d.png", jobID, snap.String(), request.DocumentIdx, request.Page)
	if err := s.putImageBlob(ctx, key, request.Body); err != nil {
		return WorkerPutSnapshotPage400JSONResponse{errBadRequest("Недопустимая картинка")}, nil
	}
	return WorkerPutSnapshotPage204Response{}, nil
}

// WorkerPutSnapshotThumb implements PUT .../snapshot/{snapshot}/thumbs/{document_idx}/{page}.
func (s *Server) WorkerPutSnapshotThumb(ctx context.Context, request WorkerPutSnapshotThumbRequestObject) (WorkerPutSnapshotThumbResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerPutSnapshotThumb401JSONResponse{errUnauthorized()}, nil
	}
	snap, err := queue.ParseSnapshot(request.Snapshot)
	if err != nil {
		return WorkerPutSnapshotThumb400JSONResponse{errBadRequest("Неверный снимок")}, nil
	}
	if request.DocumentIdx < 0 || request.Page < 1 {
		return WorkerPutSnapshotThumb400JSONResponse{errBadRequest("Неверная страница")}, nil
	}
	jobID := string(request.Id)
	if err := s.queue.CheckSnapshot(ctx, jobID, w.ID, int64(request.Params.Epoch), snap); err != nil {
		status, _, _, ok := workerErr(err)
		if !ok {
			return nil, err
		}
		if status == http.StatusNotFound {
			return WorkerPutSnapshotThumb404JSONResponse{errNotFound()}, nil
		}
		return WorkerPutSnapshotThumb409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
	}
	key := fmt.Sprintf("jobs/%s/%s/thumbs/%d/%d.png", jobID, snap.String(), request.DocumentIdx, request.Page)
	if err := s.putImageBlob(ctx, key, request.Body); err != nil {
		return WorkerPutSnapshotThumb400JSONResponse{errBadRequest("Недопустимая картинка")}, nil
	}
	return WorkerPutSnapshotThumb204Response{}, nil
}

// WorkerPutRevisionRemark implements PUT .../input/revision/{n}/remarks/{idx} —
// the crop of the marked region.
func (s *Server) WorkerPutRevisionRemark(ctx context.Context, request WorkerPutRevisionRemarkRequestObject) (WorkerPutRevisionRemarkResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerPutRevisionRemark401JSONResponse{errUnauthorized()}, nil
	}
	if request.N < 1 || request.Idx < 1 {
		return WorkerPutRevisionRemark400JSONResponse{errBadRequest("Неверное замечание")}, nil
	}
	jobID := string(request.Id)
	if err := s.queue.CheckLease(ctx, jobID, w.ID, int64(request.Params.Epoch)); err != nil {
		status, _, _, ok := workerErr(err)
		if !ok {
			return nil, err
		}
		if status == http.StatusNotFound {
			return WorkerPutRevisionRemark404JSONResponse{errNotFound()}, nil
		}
		return WorkerPutRevisionRemark409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
	}
	key := fmt.Sprintf("jobs/%s/input/revision-%d/remarks/%d.png", jobID, request.N, request.Idx)
	body := http.MaxBytesReader(responseFrom(ctx), io.NopCloser(request.Body), maxSnapshotFile)
	size, sha, err := s.blobs.Put(ctx, key, body)
	if err != nil {
		return nil, err
	}
	if err := s.queue.RecordRevisionCrop(ctx, jobID, w.ID, int64(request.Params.Epoch), int64(request.N), int64(request.Idx), key, size, sha); err != nil {
		_ = s.blobs.Delete(ctx, key)
		status, _, _, ok := workerErr(err)
		if !ok {
			return nil, err
		}
		if status == http.StatusNotFound {
			return WorkerPutRevisionRemark404JSONResponse{errNotFound()}, nil
		}
		return WorkerPutRevisionRemark409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
	}
	return WorkerPutRevisionRemark204Response{}, nil
}

// putImageBlob streams a page/thumb image into blobs.
func (s *Server) putImageBlob(ctx context.Context, key string, body io.Reader) error {
	limited := http.MaxBytesReader(responseFrom(ctx), io.NopCloser(body), maxSnapshotFile)
	_, _, err := s.blobs.Put(ctx, key, limited)
	return err
}

// WorkerCommitSnapshot implements POST /api/worker/jobs/{id}/snapshot/{snapshot}/commit.
func (s *Server) WorkerCommitSnapshot(ctx context.Context, request WorkerCommitSnapshotRequestObject) (WorkerCommitSnapshotResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerCommitSnapshot401JSONResponse{errUnauthorized()}, nil
	}
	if request.Body == nil {
		return WorkerCommitSnapshot400JSONResponse{errBadRequest("Пустое тело")}, nil
	}
	snap, err := queue.ParseSnapshot(request.Snapshot)
	if err != nil {
		return WorkerCommitSnapshot400JSONResponse{errBadRequest("Неверный снимок")}, nil
	}
	jobID := string(request.Id)
	// The lease and snapshot gates are checked up front (CheckCommit lets a
	// repeated draft commit through for the no-op path); the authoritative
	// check repeats inside the CommitSnapshot transaction.
	if err := s.queue.CheckCommit(ctx, jobID, w.ID, int64(request.Body.Epoch), snap); err != nil {
		return workerCommitErr(err)
	}
	docs, verr := s.validateSnapshot(ctx, jobID, snap, request.Body.Documents)
	if verr != nil {
		return WorkerCommitSnapshot400JSONResponse{errBadRequest(verr.Error())}, nil
	}
	// Verification JSON of a version snapshot is kept next to the bundle so
	// the admin view can show it.
	if !snap.Draft && request.Body.Verification != nil {
		raw, err := jsonString(*request.Body.Verification)
		if err == nil {
			key := fmt.Sprintf("jobs/%s/%s/verification.json", jobID, snap.String())
			if _, _, err := s.blobs.Put(ctx, key, strings.NewReader(raw)); err != nil {
				return nil, err
			}
		}
	}
	if err := s.queue.CommitSnapshot(ctx, jobID, w.ID, int64(request.Body.Epoch), snap, request.Body.Title, docs); err != nil {
		return workerCommitErr(err)
	}
	return WorkerCommitSnapshot204Response{}, nil
}

func workerCommitErr(err error) (WorkerCommitSnapshotResponseObject, error) {
	status, _, _, ok := workerErr(err)
	if !ok {
		return nil, err
	}
	if status == http.StatusNotFound {
		return WorkerCommitSnapshot404JSONResponse{errNotFound()}, nil
	}
	return WorkerCommitSnapshot409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
}

// validateSnapshot checks that every document file, preview and page/thumb
// blob of the snapshot was uploaded, and builds the document rows.
func (s *Server) validateSnapshot(ctx context.Context, jobID string, snap queue.SnapshotRef, docs []SnapshotDocument) ([]store.DocumentWithPages, error) {
	if len(docs) == 0 {
		return nil, errors.New("нужен хотя бы один документ")
	}
	seenIdx := map[int]bool{}
	for _, d := range docs {
		if d.Idx < 0 {
			return nil, fmt.Errorf("документ %q: idx не может быть отрицательным", d.Title)
		}
		if seenIdx[d.Idx] {
			return nil, fmt.Errorf("документы с одинаковым idx=%d недопустимы", d.Idx)
		}
		seenIdx[d.Idx] = true
	}
	out := make([]store.DocumentWithPages, 0, len(docs))
	for _, d := range docs {
		filePath, err := jobs.NormalizePath(d.FilePath)
		if err != nil {
			return nil, fmt.Errorf("документ %d: недопустимый путь файла", d.Idx)
		}
		if err := s.requireBlob(ctx, jobID, snap, "out/"+filePath); err != nil {
			return nil, fmt.Errorf("документ %q: файл %s не загружен", d.Title, filePath)
		}
		var previewPath *string
		if d.PreviewPath != nil && *d.PreviewPath != "" {
			p, err := jobs.NormalizePath(*d.PreviewPath)
			if err != nil || !strings.HasPrefix(p, "preview/") {
				return nil, fmt.Errorf("документ %q: недопустимый preview_path", d.Title)
			}
			if err := s.requireBlob(ctx, jobID, snap, p); err != nil {
				return nil, fmt.Errorf("документ %q: превью %s не загружено", d.Title, p)
			}
			previewPath = &p
		}
		if d.PageCount < 0 || d.PageCount > 100000 {
			return nil, fmt.Errorf("документ %q: неверное число страниц", d.Title)
		}
		if d.PageCount > 0 {
			if len(d.Pages) != d.PageCount {
				return nil, fmt.Errorf("документ %q: ожидалось %d страниц, прислано %d", d.Title, d.PageCount, len(d.Pages))
			}
		}
		doc := store.Document{
			ID:          auth.NewID(),
			Idx:         int64(d.Idx),
			Title:       d.Title,
			Kind:        d.Kind,
			FilePath:    filePath,
			PreviewPath: previewPath,
			PageCount:   int64(d.PageCount),
		}
		pages := make([]store.Page, 0, len(d.Pages))
		seen := map[int]bool{}
		for _, p := range d.Pages {
			if p.Page < 1 || p.Page > d.PageCount || seen[p.Page] {
				return nil, fmt.Errorf("документ %q: неверный номер страницы %d", d.Title, p.Page)
			}
			seen[p.Page] = true
			imageKey := fmt.Sprintf("jobs/%s/%s/pages/%d/%d.png", jobID, snap.String(), d.Idx, p.Page)
			thumbKey := fmt.Sprintf("jobs/%s/%s/thumbs/%d/%d.png", jobID, snap.String(), d.Idx, p.Page)
			if err := s.requireBlobKey(ctx, imageKey); err != nil {
				return nil, fmt.Errorf("документ %q: страница %d не загружена", d.Title, p.Page)
			}
			if err := s.requireBlobKey(ctx, thumbKey); err != nil {
				return nil, fmt.Errorf("документ %q: мини-копия страницы %d не загружена", d.Title, p.Page)
			}
			boxes := "[]"
			if p.ChangedBoxes != nil {
				if raw, err := jsonString(*p.ChangedBoxes); err == nil {
					boxes = raw
				}
			}
			pages = append(pages, store.Page{
				Page:         int64(p.Page),
				ImageKey:     imageKey,
				ThumbKey:     thumbKey,
				Width:        int64(p.Width),
				Height:       int64(p.Height),
				ChangedBoxes: boxes,
			})
		}
		out = append(out, store.DocumentWithPages{Document: doc, Pages: pages})
	}
	return out, nil
}

// requireBlob checks that a snapshot blob exists (sub is "out/" or a
// "preview/..." path).
func (s *Server) requireBlob(ctx context.Context, jobID string, snap queue.SnapshotRef, rel string) error {
	return s.requireBlobKey(ctx, fmt.Sprintf("jobs/%s/%s/%s", jobID, snap.String(), rel))
}

func (s *Server) requireBlobKey(ctx context.Context, key string) error {
	rc, _, err := s.blobs.Open(ctx, key)
	if err != nil {
		return err
	}
	return rc.Close()
}

// WorkerQuestion implements POST /api/worker/jobs/{id}/question.
func (s *Server) WorkerQuestion(ctx context.Context, request WorkerQuestionRequestObject) (WorkerQuestionResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerQuestion401JSONResponse{errUnauthorized()}, nil
	}
	if request.Body == nil || strings.TrimSpace(request.Body.Text) == "" {
		return WorkerQuestion400JSONResponse{errBadRequest("Вопрос не может быть пустым")}, nil
	}
	err := s.queue.Question(ctx, string(request.Id), w.ID, int64(request.Body.Epoch), request.Body.Text)
	if err != nil {
		status, _, _, ok := workerErr(err)
		if !ok {
			return nil, err
		}
		if status == http.StatusNotFound {
			return WorkerQuestion404JSONResponse{errNotFound()}, nil
		}
		return WorkerQuestion409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
	}
	return WorkerQuestion204Response{}, nil
}

// WorkerFinish implements POST /api/worker/jobs/{id}/finish.
func (s *Server) WorkerFinish(ctx context.Context, request WorkerFinishRequestObject) (WorkerFinishResponseObject, error) {
	w, ok := s.requireWorker(ctx, requestFrom(ctx))
	if !ok {
		return WorkerFinish401JSONResponse{errUnauthorized()}, nil
	}
	if request.Body == nil {
		return WorkerFinish400JSONResponse{errBadRequest("Пустое тело")}, nil
	}
	b := request.Body
	outcome := string(b.Outcome)
	switch outcome {
	case queue.OutcomeOK, queue.OutcomeFailed, queue.OutcomeCanceled, queue.OutcomeTimeout:
	default:
		return WorkerFinish400JSONResponse{errBadRequest("Неверный исход")}, nil
	}
	var version *int64
	if b.Version != nil {
		v := int64(*b.Version)
		version = &v
	}
	var errText string
	if b.Error != nil {
		errText = *b.Error
	}
	err := s.queue.Finish(ctx, string(request.Id), w.ID, int64(b.Epoch), outcome, version, errText)
	if err != nil {
		status, _, _, ok := workerErr(err)
		if !ok {
			return nil, err
		}
		if status == http.StatusNotFound {
			return WorkerFinish404JSONResponse{errNotFound()}, nil
		}
		return WorkerFinish409JSONResponse{StaleLeaseJSONResponse(workerErrBody(err))}, nil
	}
	return WorkerFinish204Response{}, nil
}

// ---------- helpers ----------

func int64Ptr(p *int) *int64 {
	if p == nil {
		return nil
	}
	v := int64(*p)
	return &v
}

// jsonString marshals v to a compact JSON string.
func jsonString(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func errBadBody() error { return errors.New("empty request body") }
