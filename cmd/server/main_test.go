package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckydiss/studlance_ai/internal/config"
	"github.com/luckydiss/studlance_ai/internal/store"
	"github.com/luckydiss/studlance_ai/internal/store/sqlite"
)

// TestUserCreateUsesEnvData verifies that `user create` (without --data)
// honours STUDLANCE_DATA, creating the database in that directory.
func TestUserCreateUsesEnvData(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STUDLANCE_DATA", dir)
	config.Stdin = strings.NewReader("secret\n")
	t.Cleanup(func() { config.Stdin = os.Stdin })

	err := runUser([]string{"create", "--email", "env@local", "--role", "client", "--name", "Env", "--password-stdin"})
	if err != nil {
		t.Fatalf("runUser: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "studlance.db")); err != nil {
		t.Fatalf("db not created in STUDLANCE_DATA dir: %v", err)
	}

	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(dir, "studlance.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	u, err := st.UserByEmail(ctx, "env@local")
	if err != nil {
		t.Fatalf("user not found: %v", err)
	}
	if u.Role != store.RoleClient {
		t.Fatalf("role %q", u.Role)
	}
}
