package config

import (
	"flag"
	"path/filepath"
	"testing"
)

func TestParseServerFlagsExtraFlagsAndEnvData(t *testing.T) {
	env := map[string]string{"STUDLANCE_DATA": filepath.Join("tmp", "data")}
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	email := fs.String("email", "", "email")
	cfg, err := ParseServerFlags(fs, []string{"--email", "a@b.ru"}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *email != "a@b.ru" {
		t.Fatalf("extra flag not parsed: %q", *email)
	}
	if cfg.Data != filepath.Join("tmp", "data") {
		t.Fatalf("data from env: %q", cfg.Data)
	}

	// --data overrides the env.
	fs2 := flag.NewFlagSet("backup", flag.ContinueOnError)
	out := fs2.String("out", "", "out")
	cfg2, err := ParseServerFlags(fs2, []string{"--out", "b.zip", "--data", "custom"}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("parse2: %v", err)
	}
	if *out != "b.zip" || cfg2.Data != "custom" {
		t.Fatalf("out=%q data=%q", *out, cfg2.Data)
	}
}
