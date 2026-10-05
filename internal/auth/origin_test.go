package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckOrigin(t *testing.T) {
	cases := []struct {
		name   string
		method string
		origin string
		host   string
		proto  string
		want   bool
	}{
		{"get always ok", http.MethodGet, "http://evil.test", "127.0.0.1:8080", "", true},
		{"post same origin", http.MethodPost, "http://127.0.0.1:8080", "127.0.0.1:8080", "", true},
		{"post different port", http.MethodPost, "http://127.0.0.1:9999", "127.0.0.1:8080", "", false},
		{"post origin without port", http.MethodPost, "http://127.0.0.1", "127.0.0.1:8080", "", false},
		{"post scheme mismatch", http.MethodPost, "https://127.0.0.1:8080", "127.0.0.1:8080", "", false},
		{"post foreign origin", http.MethodPost, "http://evil.test", "127.0.0.1:8080", "", false},
		{"post no origin", http.MethodPost, "", "127.0.0.1:8080", "", true},
		{"post forwarded https match", http.MethodPost, "https://example.test", "example.test", "https", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "/api/auth/login", nil)
			r.Host = c.host
			if c.proto != "" {
				r.Header.Set("X-Forwarded-Proto", c.proto)
			}
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if got := CheckOrigin(r); got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}
