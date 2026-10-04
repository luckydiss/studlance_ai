package config

import (
	"testing"
	"time"
)

func TestParseServerDefaults(t *testing.T) {
	cfg, err := ParseServer(nil, func(string) string { return "" })
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Addr != "127.0.0.1:8080" || cfg.Data != "./data" {
		t.Fatalf("defaults: %+v", cfg)
	}
	if cfg.SessionTTL != 720*time.Hour || cfg.StageTimeout != 3*time.Hour {
		t.Fatalf("durations: %+v", cfg)
	}
	if cfg.MaxUpload != 2<<30 {
		t.Fatalf("max upload %d", cfg.MaxUpload)
	}
}

func TestParseServerEnvAndFlags(t *testing.T) {
	env := map[string]string{
		"STUDLANCE_ADDR":          "0.0.0.0:9000",
		"STUDLANCE_SESSION_TTL":   "1h",
		"STUDLANCE_COOKIE_SECURE": "true",
		"STUDLANCE_MAX_UPLOAD":    "512MB",
		"STUDLANCE_STAGE_TIMEOUT": "30m",
	}
	cfg, err := ParseServer([]string{"--data", "/tmp/x"}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Addr != "0.0.0.0:9000" || cfg.Data != "/tmp/x" {
		t.Fatalf("cfg %+v", cfg)
	}
	if !cfg.CookieSecure || cfg.SessionTTL != time.Hour || cfg.StageTimeout != 30*time.Minute {
		t.Fatalf("cfg %+v", cfg)
	}
	if cfg.MaxUpload != 512<<20 {
		t.Fatalf("max upload %d", cfg.MaxUpload)
	}
}

func TestParseByteSize(t *testing.T) {
	cases := map[string]int64{
		"2GB":  2 << 30,
		"1MB":  1 << 20,
		"1KB":  1 << 10,
		"100B": 100,
		"42":   42,
	}
	for in, want := range cases {
		got, err := parseByteSize(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Fatalf("%s: got %d want %d", in, got, want)
		}
	}
	if _, err := parseByteSize("nope"); err == nil {
		t.Fatal("expected error")
	}
}
