// Package auth implements sessions, login rate limiting and role middleware.
package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/luckydiss/studlance_ai/internal/id"
	"github.com/luckydiss/studlance_ai/internal/store"
	"github.com/luckydiss/studlance_ai/internal/token"
)

// SessionCookie is the session cookie name (04-api.md).
const SessionCookie = "sl_session"

// Login limits (04-api.md): max 10 failed attempts per 15 minutes per IP and per email.
const (
	MaxLoginAttempts = 10
	LoginWindow      = 15 * time.Minute
)

// Config configures the auth service.
type Config struct {
	SessionTTL   time.Duration
	CookieSecure bool
}

// ErrRateLimited is returned when too many failed attempts occurred.
var ErrRateLimited = errors.New("rate limited")

// Service authenticates users and manages sessions.
type Service struct {
	store store.Store
	cfg   Config
	now   func() time.Time
}

// New creates an auth service.
func New(st store.Store, cfg Config) *Service {
	return &Service{store: st, cfg: cfg, now: func() time.Time { return time.Now().UTC() }}
}

// User is the API-facing user object.
type User struct {
	ID    string
	Email string
	Name  string
	Role  store.Role
}

// SessionUser is a resolved session with its token hash.
type SessionUser struct {
	Session store.Session
	User    store.User
}

// Login verifies credentials and creates a session. It returns ErrRateLimited
// when the IP or email exceeded the attempt limit, and ErrInvalidCredentials
// when the password is wrong (recording a failed attempt).
var ErrInvalidCredentials = errors.New("invalid credentials")

func (s *Service) Login(ctx context.Context, email, password, ip, userAgent string) (store.Session, string, store.User, error) {
	now := s.now()
	emailKey := "email:" + strings.ToLower(email)
	ipKey := "ip:" + ip

	for _, key := range []string{ipKey, emailKey} {
		n, cErr := s.store.CountLoginAttempts(ctx, key, now.Add(-LoginWindow))
		if cErr != nil {
			return store.Session{}, "", store.User{}, cErr
		}
		if n >= MaxLoginAttempts {
			return store.Session{}, "", store.User{}, ErrRateLimited
		}
	}

	found, fErr := s.store.UserByEmail(ctx, email)
	if fErr != nil || !validPassword(found, password) || found.DisabledAt != nil {
		_ = s.store.CreateLoginAttempt(ctx, ipKey, now)
		_ = s.store.CreateLoginAttempt(ctx, emailKey, now)
		return store.Session{}, "", store.User{}, ErrInvalidCredentials
	}

	secret := token.New()
	sess := store.Session{
		IDHash:     token.Hash(secret),
		UserID:     found.ID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.cfg.SessionTTL),
		LastSeenAt: now,
		UserAgent:  userAgent,
		IP:         ip,
	}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return store.Session{}, "", store.User{}, err
	}
	_ = s.store.ClearLoginAttempts(ctx, ipKey)
	_ = s.store.ClearLoginAttempts(ctx, emailKey)
	return sess, secret, found, nil
}

func validPassword(u store.User, password string) bool {
	if u.PasswordHash == "" {
		return false
	}
	ok, err := VerifyPassword(password, u.PasswordHash)
	return err == nil && ok
}

// Resolve returns the user for a session cookie value, or ErrNotFound.
func (s *Service) Resolve(ctx context.Context, secret string) (SessionUser, error) {
	if secret == "" {
		return SessionUser{}, store.ErrNotFound
	}
	hash := token.Hash(secret)
	sess, err := s.store.SessionByIDHash(ctx, hash)
	if err != nil {
		return SessionUser{}, err
	}
	now := s.now()
	if !sess.ExpiresAt.After(now) {
		_ = s.store.DeleteSession(ctx, hash)
		return SessionUser{}, store.ErrNotFound
	}
	// Refresh last_seen_at at most once per minute.
	if now.Sub(sess.LastSeenAt) > time.Minute {
		_ = s.store.TouchSession(ctx, hash, now)
		sess.LastSeenAt = now
	}
	u, err := s.store.UserByID(ctx, sess.UserID)
	if err != nil || u.DisabledAt != nil {
		return SessionUser{}, store.ErrNotFound
	}
	return SessionUser{Session: sess, User: u}, nil
}

// Logout deletes a session.
func (s *Service) Logout(ctx context.Context, secret string) error {
	if secret == "" {
		return nil
	}
	return s.store.DeleteSession(ctx, token.Hash(secret))
}

// SetCookie writes the session cookie.
func (s *Service) SetCookie(w http.ResponseWriter, secret string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    secret,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie removes the session cookie.
func (s *Service) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// SessionSecret returns the raw cookie value from a request.
func SessionSecret(r *http.Request) string {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// CheckOrigin enforces that non-GET requests to the API come from the server's
// own origin (04-api.md, CSRF).
func CheckOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No Origin header: allow same-origin clients without CORS (e.g. curl).
		return true
	}
	u := origin
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimPrefix(u, "https://")
	host := r.Host
	if u == host {
		return true
	}
	// Allow origin host without port matching the request host.
	oh, _, err := net.SplitHostPort(u)
	if err == nil {
		rh, _, rerr := net.SplitHostPort(host)
		if rerr == nil {
			return oh == rh
		}
	}
	return false
}

// NewID exposes id generation for handlers that need it (kept here to avoid
// direct internal/id imports in httpapi).
func NewID() string { return id.New() }
