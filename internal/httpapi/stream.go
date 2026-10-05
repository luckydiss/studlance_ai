package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/luckydiss/studlance_ai/internal/auth"
	"github.com/luckydiss/studlance_ai/internal/live"
	"github.com/luckydiss/studlance_ai/internal/store"
)

// ClientJobStream implements GET /api/client/jobs/{id}/stream.
func (s *Server) ClientJobStream(ctx context.Context, request ClientJobStreamRequestObject) (ClientJobStreamResponseObject, error) {
	j, ok := s.clientJob(ctx, string(request.Id), requestFrom(ctx))
	if !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return ClientJobStream401JSONResponse{errUnauthorized()}, nil
		}
		return ClientJobStream404JSONResponse{errNotFound()}, nil
	}
	return clientStreamResponse{server: s, jobID: j.ID, req: requestFrom(ctx)}, nil
}

// AdminJobStream implements GET /api/admin/jobs/{id}/stream.
func (s *Server) AdminJobStream(ctx context.Context, request AdminJobStreamRequestObject) (AdminJobStreamResponseObject, error) {
	if _, ok := s.requireAdmin(ctx, requestFrom(ctx)); !ok {
		if !s.authenticated(ctx, requestFrom(ctx)) {
			return AdminJobStream401JSONResponse{errUnauthorized()}, nil
		}
		return AdminJobStream403JSONResponse{errForbidden()}, nil
	}
	j, err := s.store.JobByID(ctx, string(request.Id))
	if err != nil {
		return AdminJobStream404JSONResponse{errNotFound()}, nil
	}
	return adminStreamResponse{server: s, jobID: j.ID, req: requestFrom(ctx)}, nil
}

type clientStreamResponse struct {
	server *Server
	jobID  string
	req    *http.Request
}

func (resp clientStreamResponse) VisitClientJobStreamResponse(w http.ResponseWriter) error {
	resp.server.stream(w, resp.req, resp.jobID, false)
	return nil
}

type adminStreamResponse struct {
	server *Server
	jobID  string
	req    *http.Request
}

func (resp adminStreamResponse) VisitAdminJobStreamResponse(w http.ResponseWriter) error {
	resp.server.stream(w, resp.req, resp.jobID, true)
	return nil
}

// stream serves an SSE stream: snapshot first, then job events, ping every 20s.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, jobID string, admin bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "Внутренняя ошибка")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sub := s.hub.Subscribe(jobID, 16)
	defer sub.Close()

	ctx := r.Context()

	if err := s.writeSnapshot(ctx, w, jobID, admin); err != nil {
		return
	}
	flusher.Flush()

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			if ev.Name == "steps" {
				_, _ = fmt.Fprintf(w, "event: steps\ndata: %s\n\n", ev.Data)
				flusher.Flush()
				continue
			}
			if err := s.writeJobEvent(ctx, w, jobID, admin); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSnapshot sends the initial snapshot event.
func (s *Server) writeSnapshot(ctx context.Context, w http.ResponseWriter, jobID string, admin bool) error {
	data, err := s.snapshotJSON(ctx, jobID, admin)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", data)
	return err
}

func (s *Server) writeJobEvent(ctx context.Context, w http.ResponseWriter, jobID string, admin bool) error {
	data, err := s.snapshotJSON(ctx, jobID, admin)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: job\ndata: %s\n\n", data)
	return err
}

func (s *Server) snapshotJSON(ctx context.Context, jobID string, admin bool) ([]byte, error) {
	j, err := s.store.JobByID(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if admin {
		detail, dErr := s.adminJobDetail(ctx, j)
		if dErr != nil {
			return nil, dErr
		}
		return json.Marshal(detail)
	}
	detail, dErr := s.clientJobDetail(ctx, j)
	if dErr != nil {
		return nil, dErr
	}
	return json.Marshal(detail)
}

var (
	_ = auth.SessionCookie
	_ = live.Event{}
	_ = store.StatusDone
)
