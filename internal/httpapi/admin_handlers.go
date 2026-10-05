package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/luckydiss/studlance_ai/internal/auth"
	"github.com/luckydiss/studlance_ai/internal/store"
)

// AdminListJobs implements GET /api/admin/jobs.
func (s *Server) AdminListJobs(ctx context.Context, request AdminListJobsRequestObject) (AdminListJobsResponseObject, error) {
	if _, ok := s.requireAdmin(ctx, requestFrom(ctx)); !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return AdminListJobs401JSONResponse{errUnauthorized()}, nil
		}
		return AdminListJobs403JSONResponse{errForbidden()}, nil
	}
	filter := store.JobFilter{}
	if request.Params.Status != nil {
		filter.Status = *request.Params.Status
	}
	if request.Params.Attention != nil && *request.Params.Attention == 1 {
		filter.Attention = true
	}
	if request.Params.Q != nil {
		filter.Query = *request.Params.Q
	}
	rows, err := s.store.AdminJobs(ctx, filter)
	if err != nil {
		return nil, err
	}
	out := make([]AdminJobSummary, 0, len(rows))
	for _, j := range rows {
		out = append(out, s.adminJobSummary(ctx, j))
	}
	return AdminListJobs200JSONResponse(AdminJobList{Jobs: out}), nil
}

// AdminGetJob implements GET /api/admin/jobs/{id}.
func (s *Server) AdminGetJob(ctx context.Context, request AdminGetJobRequestObject) (AdminGetJobResponseObject, error) {
	if _, ok := s.requireAdmin(ctx, requestFrom(ctx)); !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return AdminGetJob401JSONResponse{errUnauthorized()}, nil
		}
		return AdminGetJob403JSONResponse{errForbidden()}, nil
	}
	j, err := s.store.JobByID(ctx, string(request.Id))
	if err != nil {
		return AdminGetJob404JSONResponse{errNotFound()}, nil
	}
	detail, dErr := s.adminJobDetail(ctx, j)
	if dErr != nil {
		return nil, dErr
	}
	return AdminGetJob200JSONResponse(detail), nil
}

// AdminGetRunSteps implements GET /api/admin/jobs/{id}/runs/{run_id}/steps.
func (s *Server) AdminGetRunSteps(ctx context.Context, request AdminGetRunStepsRequestObject) (AdminGetRunStepsResponseObject, error) {
	if _, ok := s.requireAdmin(ctx, requestFrom(ctx)); !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return AdminGetRunSteps401JSONResponse{errUnauthorized()}, nil
		}
		return AdminGetRunSteps403JSONResponse{errForbidden()}, nil
	}
	if _, err := s.store.AgentRunByID(ctx, request.RunId, string(request.Id)); err != nil {
		return AdminGetRunSteps404JSONResponse{errNotFound()}, nil
	}
	after := int64(0)
	if request.Params.AfterSeq != nil {
		after = int64(*request.Params.AfterSeq)
	}
	steps, err := s.store.TraceStepsByRun(ctx, request.RunId, after, 500)
	if err != nil {
		return nil, err
	}
	out := make([]TraceStep, 0, len(steps))
	for _, st := range steps {
		payload := map[string]interface{}{}
		if st.Payload != "" {
			_ = json.Unmarshal([]byte(st.Payload), &payload)
		}
		out = append(out, TraceStep{Seq: int(st.Seq), Ts: st.Ts, Type: st.Type, Summary: st.Summary, Payload: payload})
	}
	return AdminGetRunSteps200JSONResponse(TraceStepList{Steps: out}), nil
}

// AdminGetRunLog implements GET /api/admin/jobs/{id}/runs/{run_id}/log.
func (s *Server) AdminGetRunLog(ctx context.Context, request AdminGetRunLogRequestObject) (AdminGetRunLogResponseObject, error) {
	if _, ok := s.requireAdmin(ctx, requestFrom(ctx)); !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return AdminGetRunLog401JSONResponse{errUnauthorized()}, nil
		}
		return AdminGetRunLog403JSONResponse{errForbidden()}, nil
	}
	run, err := s.store.AgentRunByID(ctx, request.RunId, string(request.Id))
	if err != nil || run.RawLogKey == nil {
		return AdminGetRunLog404JSONResponse{errNotFound()}, nil
	}
	rc, info, oErr := s.blobs.Open(ctx, *run.RawLogKey)
	if oErr != nil {
		return AdminGetRunLog404JSONResponse{errNotFound()}, nil
	}
	return adminLogResponse{body: rc, size: info.Size, req: requestFrom(ctx)}, nil
}

// AdminAnswerJob implements POST /api/admin/jobs/{id}/answer.
func (s *Server) AdminAnswerJob(ctx context.Context, request AdminAnswerJobRequestObject) (AdminAnswerJobResponseObject, error) {
	if _, ok := s.requireAdmin(ctx, requestFrom(ctx)); !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return AdminAnswerJob401JSONResponse{errUnauthorized()}, nil
		}
		return AdminAnswerJob403JSONResponse{errForbidden()}, nil
	}
	j, err := s.store.JobByID(ctx, string(request.Id))
	if err != nil {
		return AdminAnswerJob404JSONResponse{errNotFound()}, nil
	}
	if request.Body == nil || strings.TrimSpace(request.Body.Text) == "" {
		return AdminAnswerJob400JSONResponse{errBadRequest("Ответ не может быть пустым")}, nil
	}
	if j.Status != store.StatusNeedsInput {
		return AdminAnswerJob409JSONResponse{errConflict("Сейчас отвечать не требуется")}, nil
	}
	if err := s.applyAnswer(ctx, j, request.Body.Text, "admin"); err != nil {
		return nil, err
	}
	updated, _ := s.store.JobByID(ctx, j.ID)
	s.publishJob(ctx, updated)
	detail, _, dErr := s.adminDetail(ctx, updated.ID)
	if dErr != nil {
		return nil, dErr
	}
	return AdminAnswerJob200JSONResponse(detail), nil
}

// AdminRetryJob implements POST /api/admin/jobs/{id}/retry.
func (s *Server) AdminRetryJob(ctx context.Context, request AdminRetryJobRequestObject) (AdminRetryJobResponseObject, error) {
	if _, ok := s.requireAdmin(ctx, requestFrom(ctx)); !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return AdminRetryJob401JSONResponse{errUnauthorized()}, nil
		}
		return AdminRetryJob403JSONResponse{errForbidden()}, nil
	}
	j, err := s.store.JobByID(ctx, string(request.Id))
	if err != nil {
		return AdminRetryJob404JSONResponse{errNotFound()}, nil
	}
	if j.Status != store.StatusFailed {
		return AdminRetryJob409JSONResponse{errConflict("Повторить можно только заказ со сбоем")}, nil
	}
	j.Status = store.StatusQueued
	j.Attempt = 0
	j.Error = nil
	j.UpdatedAt = s.now()
	if err := s.store.UpdateJob(ctx, j); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, j.ID, "retried", false, map[string]interface{}{"by": "admin"})
	s.publishJob(ctx, j)
	detail, _, dErr := s.adminDetail(ctx, j.ID)
	if dErr != nil {
		return nil, dErr
	}
	return AdminRetryJob200JSONResponse(detail), nil
}

// AdminCancelJob implements POST /api/admin/jobs/{id}/cancel.
func (s *Server) AdminCancelJob(ctx context.Context, request AdminCancelJobRequestObject) (AdminCancelJobResponseObject, error) {
	if _, ok := s.requireAdmin(ctx, requestFrom(ctx)); !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return AdminCancelJob401JSONResponse{errUnauthorized()}, nil
		}
		return AdminCancelJob403JSONResponse{errForbidden()}, nil
	}
	j, err := s.store.JobByID(ctx, string(request.Id))
	if err != nil {
		return AdminCancelJob404JSONResponse{errNotFound()}, nil
	}
	if !canCancelInternal(j) {
		return AdminCancelJob409JSONResponse{errConflict("Заказ нельзя отменить")}, nil
	}
	if err := s.applyCancel(ctx, j); err != nil {
		return nil, err
	}
	updated, _ := s.store.JobByID(ctx, j.ID)
	s.publishJob(ctx, updated)
	detail, _, dErr := s.adminDetail(ctx, updated.ID)
	if dErr != nil {
		return nil, dErr
	}
	return AdminCancelJob200JSONResponse(detail), nil
}

// AdminSetAttention implements POST /api/admin/jobs/{id}/attention.
func (s *Server) AdminSetAttention(ctx context.Context, request AdminSetAttentionRequestObject) (AdminSetAttentionResponseObject, error) {
	if _, ok := s.requireAdmin(ctx, requestFrom(ctx)); !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return AdminSetAttention401JSONResponse{errUnauthorized()}, nil
		}
		return AdminSetAttention403JSONResponse{errForbidden()}, nil
	}
	j, err := s.store.JobByID(ctx, string(request.Id))
	if err != nil {
		return AdminSetAttention404JSONResponse{errNotFound()}, nil
	}
	if request.Body == nil {
		return AdminSetAttention400JSONResponse{errBadRequest("Пустое тело")}, nil
	}
	j.NeedsAttention = request.Body.NeedsAttention
	j.UpdatedAt = s.now()
	if err := s.store.UpdateJob(ctx, j); err != nil {
		return nil, err
	}
	s.publishJob(ctx, j)
	detail, _, dErr := s.adminDetail(ctx, j.ID)
	if dErr != nil {
		return nil, dErr
	}
	return AdminSetAttention200JSONResponse(detail), nil
}

// AdminCreateNote implements POST /api/admin/jobs/{id}/notes.
func (s *Server) AdminCreateNote(ctx context.Context, request AdminCreateNoteRequestObject) (AdminCreateNoteResponseObject, error) {
	su, ok := s.requireAdmin(ctx, requestFrom(ctx))
	if !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return AdminCreateNote401JSONResponse{errUnauthorized()}, nil
		}
		return AdminCreateNote403JSONResponse{errForbidden()}, nil
	}
	j, err := s.store.JobByID(ctx, string(request.Id))
	if err != nil {
		return AdminCreateNote404JSONResponse{errNotFound()}, nil
	}
	if request.Body == nil || strings.TrimSpace(request.Body.Text) == "" {
		return AdminCreateNote400JSONResponse{errBadRequest("Заметка не может быть пустой")}, nil
	}
	n := store.Note{ID: auth.NewID(), JobID: j.ID, AuthorID: su.User.ID, Text: request.Body.Text, CreatedAt: s.now()}
	if err := s.store.CreateNote(ctx, n); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, j.ID, "note", false, map[string]interface{}{"note_id": n.ID})
	return AdminCreateNote201JSONResponse(JobNote{Id: n.ID, Author: su.User.ID, Text: n.Text, CreatedAt: n.CreatedAt}), nil
}

// AdminListWorkers implements GET /api/admin/workers.
func (s *Server) AdminListWorkers(ctx context.Context, request AdminListWorkersRequestObject) (AdminListWorkersResponseObject, error) {
	if _, ok := s.requireAdmin(ctx, requestFrom(ctx)); !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return AdminListWorkers401JSONResponse{errUnauthorized()}, nil
		}
		return AdminListWorkers403JSONResponse{errForbidden()}, nil
	}
	workers, err := s.store.ListWorkers(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]WorkerStatus, 0, len(workers))
	for _, w := range workers {
		online := w.LastSeenAt != nil && now.Sub(*w.LastSeenAt) < 45*time.Second
		info := map[string]interface{}{}
		if w.Info != "" {
			_ = json.Unmarshal([]byte(w.Info), &info)
		}
		caps := []string{}
		if w.Capabilities != "" {
			_ = json.Unmarshal([]byte(w.Capabilities), &caps)
		}
		out = append(out, WorkerStatus{
			Id: w.ID, Name: w.Name, Capabilities: caps, Info: info,
			LastSeenAt: w.LastSeenAt, Online: online,
		})
	}
	return AdminListWorkers200JSONResponse(WorkerList(out)), nil
}

// AdminListClients implements GET /api/admin/clients.
func (s *Server) AdminListClients(ctx context.Context, request AdminListClientsRequestObject) (AdminListClientsResponseObject, error) {
	if _, ok := s.requireAdmin(ctx, requestFrom(ctx)); !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return AdminListClients401JSONResponse{errUnauthorized()}, nil
		}
		return AdminListClients403JSONResponse{errForbidden()}, nil
	}
	clients, err := s.store.Clients(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ClientAccount, 0, len(clients))
	for _, c := range clients {
		out = append(out, ClientAccount{
			Id: c.ID, Email: c.Email, Name: c.Name, JobsTotal: int(c.JobsTotal), LastJobAt: c.LastJobAt,
		})
	}
	return AdminListClients200JSONResponse(ClientAccountList(out)), nil
}

// adminDetail loads a job and builds its admin detail.
func (s *Server) adminDetail(ctx context.Context, jobID string) (AdminJobDetail, bool, error) {
	j, err := s.store.JobByID(ctx, jobID)
	if err != nil {
		return AdminJobDetail{}, false, nil
	}
	detail, dErr := s.adminJobDetail(ctx, j)
	if dErr != nil {
		return AdminJobDetail{}, false, dErr
	}
	return detail, true, nil
}

func canCancelInternal(j store.Job) bool {
	switch j.Status {
	case store.StatusUploading, store.StatusQueued, store.StatusNeedsInput, store.StatusRunning:
		return true
	default:
		return false
	}
}
