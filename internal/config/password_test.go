package config

import (
	"strings"
	"testing"
)

func TestReadPasswordFirstLine(t *testing.T) {
	// Extra data after the first newline must not be read (no EOF wait).
	got, err := ReadPassword(strings.NewReader("secret\nignored"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "secret" {
		t.Fatalf("got %q", got)
	}
}

func TestReadPasswordTrimsCRLF(t *testing.T) {
	got, err := ReadPassword(strings.NewReader("secret\r\nrest"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "secret" {
		t.Fatalf("got %q", got)
	}
}

func TestReadPasswordEOFWithoutNewline(t *testing.T) {
	got, err := ReadPassword(strings.NewReader("secret"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "secret" {
		t.Fatalf("got %q", got)
	}
}

func TestReadPasswordEmpty(t *testing.T) {
	if _, err := ReadPassword(strings.NewReader("\n")); err == nil {
		t.Fatal("expected error for empty password")
	}
}
