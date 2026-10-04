package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/luckydiss/studlance_ai/internal/auth"
	"github.com/luckydiss/studlance_ai/internal/blobs"
	"github.com/luckydiss/studlance_ai/internal/config"
	"github.com/luckydiss/studlance_ai/internal/jobs"
	"github.com/luckydiss/studlance_ai/internal/live"
	"github.com/luckydiss/studlance_ai/internal/store"
)

// Server implements the generated StrictServerInterface.
type Server struct {
	store  store.Store
	blobs  blobs.Blobs
	auth   *auth.Service
	hub    *live.Hub
	cfg    config.Server
	logger *slog.Logger
	now    func() time.Time
}

// New creates the API server.
func New(st store.Store, b blobs.Blobs, a *auth.Service, hub *live.Hub, cfg config.Server, logger *slog.Logger) *Server {
	return &Server{store: st, blobs: b, auth: a, hub: hub, cfg: cfg, logger: logger, now: func() time.Time { return time.Now().UTC() }}
}

// requestKey stores the raw *http.Request during a strict handler call.
type requestKey struct{}

// responseKey stores the raw http.ResponseWriter during a strict handler call.
type responseKey struct{}

// requestFrom returns the raw request attached by the context middleware.
func requestFrom(ctx context.Context) *http.Request {
	r, _ := ctx.Value(requestKey{}).(*http.Request)
	return r
}

// responseFrom returns the raw response writer attached by the middleware.
func responseFrom(ctx context.Context) http.ResponseWriter {
	w, _ := ctx.Value(responseKey{}).(http.ResponseWriter)
	return w
}

// contextMiddleware attaches r and w to the context for strict handlers.
func contextMiddleware(f StrictHandlerFunc, operationID string) StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request interface{}) (interface{}, error) {
		ctx = context.WithValue(ctx, requestKey{}, r)
		ctx = context.WithValue(ctx, responseKey{}, w)
		return f(ctx, w, r, request)
	}
}

// Handler returns the strict HTTP handler wrapped with the context middleware.
func (s *Server) Handler() http.Handler {
	return Handler(NewStrictHandlerWithOptions(s, []StrictMiddlewareFunc{contextMiddleware}, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "bad_request", "Неверный запрос")
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			s.logger.Error("response error", "err", err, "path", r.URL.Path)
			writeError(w, http.StatusInternalServerError, "internal", "Внутренняя ошибка")
		},
	}))
}

// ---------- error helpers ----------

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: ErrorBody{Code: code, Message: message}})
}

func errBadRequest(msg string) BadRequestJSONResponse {
	return BadRequestJSONResponse(ErrorResponse{Error: ErrorBody{Code: "bad_request", Message: msg}})
}

func errUnauthorized() UnauthorizedJSONResponse {
	return UnauthorizedJSONResponse(ErrorResponse{Error: ErrorBody{Code: "unauthorized", Message: "Требуется вход"}})
}

func errForbidden() ForbiddenJSONResponse {
	return ForbiddenJSONResponse(ErrorResponse{Error: ErrorBody{Code: "forbidden", Message: "Доступ запрещён"}})
}

func errNotFound() NotFoundJSONResponse {
	return NotFoundJSONResponse(ErrorResponse{Error: ErrorBody{Code: "not_found", Message: "Не найдено"}})
}

func errConflict(msg string) ConflictJSONResponse {
	return ConflictJSONResponse(ErrorResponse{Error: ErrorBody{Code: "conflict", Message: msg}})
}

func errTooLarge(msg string) TooLargeJSONResponse {
	return TooLargeJSONResponse(ErrorResponse{Error: ErrorBody{Code: "too_large", Message: msg}})
}

func errRateLimited() RateLimitedJSONResponse {
	return RateLimitedJSONResponse(ErrorResponse{Error: ErrorBody{Code: "rate_limited", Message: "Слишком много попыток входа. Попробуйте позже."}})
}

// ---------- auth middleware ----------

// currentUser resolves the session user from the request, if any.
func (s *Server) currentUser(ctx context.Context, r *http.Request) (auth.SessionUser, bool) {
	if r == nil {
		return auth.SessionUser{}, false
	}
	secret := auth.SessionSecret(r)
	if secret == "" {
		return auth.SessionUser{}, false
	}
	su, err := s.auth.Resolve(ctx, secret)
	if err != nil {
		return auth.SessionUser{}, false
	}
	return su, true
}

// requireClient returns the session user when the role is client.
func (s *Server) requireClient(ctx context.Context, r *http.Request) (auth.SessionUser, bool) {
	su, ok := s.currentUser(ctx, r)
	if !ok || su.User.Role != store.RoleClient {
		return auth.SessionUser{}, false
	}
	return su, true
}

// requireAdmin returns the session user when the role is admin.
func (s *Server) requireAdmin(ctx context.Context, r *http.Request) (auth.SessionUser, bool) {
	su, ok := s.currentUser(ctx, r)
	if !ok || su.User.Role != store.RoleAdmin {
		return auth.SessionUser{}, false
	}
	return su, true
}

// ---------- shared helpers ----------

// userResponse maps a store user to the API object.
func userResponse(u store.User) UserResponse {
	return UserResponse{User: User{Id: u.ID, Email: u.Email, Name: u.Name, Role: UserRole(u.Role)}}
}

// eventData decodes an event's JSON data into a map.
func eventData(raw string) map[string]interface{} {
	m := map[string]interface{}{}
	if raw == "" {
		return m
	}
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}

// stepStateType maps our string step state to the generated enum.
func stepStateType(s string) StatusStepState { return StatusStepState(s) }

// clientStatusType maps jobs.ClientStatus to the generated enum.
func clientStatusType(s jobs.ClientStatus) ClientJobDetailClientStatus {
	return ClientJobDetailClientStatus(s)
}

// jobSummaryClientStatus maps jobs.ClientStatus to JobSummary enum.
func jobSummaryClientStatus(s jobs.ClientStatus) JobSummaryClientStatus {
	return JobSummaryClientStatus(s)
}
