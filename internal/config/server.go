// Package config holds server and worker configuration.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

// Server is the studlance-server configuration. Every flag has an env fallback.
type Server struct {
	Addr         string
	Data         string
	SessionTTL   time.Duration
	CookieSecure bool
	StageTimeout time.Duration
	MaxUpload    int64
}

// DefaultServer returns the documented defaults (01-repo-build.md).
func DefaultServer() Server {
	return Server{
		Addr:         "127.0.0.1:8080",
		Data:         "./data",
		SessionTTL:   720 * time.Hour,
		CookieSecure: false,
		StageTimeout: 3 * time.Hour,
		MaxUpload:    2 << 30, // 2 GiB
	}
}

// ParseServer parses server flags with env fallbacks. args excludes the
// program name; the command (serve, migrate, ...) is expected to be removed
// by the caller before calling ParseServer.
func ParseServer(args []string, getenv func(string) string) (Server, error) {
	cfg := DefaultServer()
	fs := flag.NewFlagSet("server", flag.ContinueOnError)

	fs.StringVar(&cfg.Addr, "addr", envString(getenv, "STUDLANCE_ADDR", cfg.Addr), "HTTP listen address")
	fs.StringVar(&cfg.Data, "data", envString(getenv, "STUDLANCE_DATA", cfg.Data), "data directory")
	fs.DurationVar(&cfg.SessionTTL, "session-ttl", envDuration(getenv, "STUDLANCE_SESSION_TTL", cfg.SessionTTL), "session TTL")
	fs.BoolVar(&cfg.CookieSecure, "cookie-secure", envBool(getenv, "STUDLANCE_COOKIE_SECURE", cfg.CookieSecure), "set Secure on session cookie")
	fs.DurationVar(&cfg.StageTimeout, "stage-timeout", envDuration(getenv, "STUDLANCE_STAGE_TIMEOUT", cfg.StageTimeout), "stage timeout")
	maxUpload := fs.String("max-upload", envString(getenv, "STUDLANCE_MAX_UPLOAD", "2GB"), "total upload limit per order (e.g. 2GB)")

	if err := fs.Parse(args); err != nil {
		return Server{}, err
	}
	size, err := parseByteSize(*maxUpload)
	if err != nil {
		return Server{}, fmt.Errorf("max-upload: %w", err)
	}
	cfg.MaxUpload = size
	return cfg, nil
}

// ServeArgs tells whether args request the given command and returns the rest.
func ServeArgs(args []string, command string) ([]string, bool) {
	if len(args) == 0 || args[0] != command {
		return args, false
	}
	return args[1:], true
}

func envString(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(getenv func(string) string, key string, fallback time.Duration) time.Duration {
	if v := getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envBool(getenv func(string) string, key string, fallback bool) bool {
	if v := getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

// parseByteSize parses "2GB", "512MB", "1024" (bytes) into bytes.
func parseByteSize(s string) (int64, error) {
	s = trimSpace(s)
	if s == "" {
		return 0, errors.New("empty")
	}
	upper := toUpper(s)
	mult := int64(1)
	num := upper
	switch {
	case hasSuffix(upper, "GB"):
		mult = 1 << 30
		num = upper[:len(upper)-2]
	case hasSuffix(upper, "MB"):
		mult = 1 << 20
		num = upper[:len(upper)-2]
	case hasSuffix(upper, "KB"):
		mult = 1 << 10
		num = upper[:len(upper)-2]
	case hasSuffix(upper, "B"):
		num = upper[:len(upper)-1]
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, errors.New("negative")
	}
	return n * mult, nil
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func toUpper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// ReadPassword reads a password from r (used for --password-stdin).
func ReadPassword(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return "", err
	}
	s := string(data)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	if s == "" {
		return "", errors.New("empty password")
	}
	return s, nil
}

// Stdin is os.Stdin, kept as a variable for tests.
var Stdin io.Reader = os.Stdin
