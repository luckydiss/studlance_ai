package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/luckydiss/studlance_ai/internal/auth"
)

// Login implements POST /api/auth/login.
func (s *Server) Login(ctx context.Context, request LoginRequestObject) (LoginResponseObject, error) {
	if request.Body == nil || request.Body.Email == "" || request.Body.Password == "" {
		return Login401JSONResponse{errUnauthorized()}, nil
	}
	r := requestFrom(ctx)
	ip := ""
	ua := ""
	if r != nil {
		ip = remoteIP(r)
		ua = r.UserAgent()
	}
	sess, secret, u, err := s.auth.Login(ctx, request.Body.Email, request.Body.Password, ip, ua)
	if err != nil {
		if errors.Is(err, auth.ErrRateLimited) {
			return Login429JSONResponse{errRateLimited()}, nil
		}
		if errors.Is(err, auth.ErrInvalidCredentials) {
			return Login401JSONResponse{UnauthorizedJSONResponse(ErrorResponse{Error: ErrorBody{Code: "unauthorized", Message: "Неверная почта или пароль"}})}, nil
		}
		s.logger.Error("login", "err", err)
		return nil, err
	}
	cookie := sessionCookieHeader(s.auth, secret, sess.ExpiresAt)
	return Login200JSONResponse{
		Body:    userResponse(u),
		Headers: Login200ResponseHeaders{SetCookie: cookie},
	}, nil
}

// Logout implements POST /api/auth/logout.
func (s *Server) Logout(ctx context.Context, request LogoutRequestObject) (LogoutResponseObject, error) {
	if r := requestFrom(ctx); r != nil {
		if secret := auth.SessionSecret(r); secret != "" {
			_ = s.auth.Logout(ctx, secret)
		}
	}
	if w := responseFrom(ctx); w != nil {
		s.auth.ClearCookie(w)
	}
	return Logout204Response{}, nil
}

// Me implements GET /api/auth/me.
func (s *Server) Me(ctx context.Context, request MeRequestObject) (MeResponseObject, error) {
	su, ok := s.currentUser(ctx, requestFrom(ctx))
	if !ok {
		return Me401JSONResponse{errUnauthorized()}, nil
	}
	return Me200JSONResponse(userResponse(su.User)), nil
}

func remoteIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func sessionCookieHeader(a *auth.Service, secret string, expires time.Time) string {
	rec := httptest.NewRecorder()
	a.SetCookie(rec, secret, expires)
	return rec.Header().Get("Set-Cookie")
}
