package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/luckydiss/studlance_ai/internal/store"
	"github.com/luckydiss/studlance_ai/internal/store/sqlite"
)

func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func TestMigrateEmptyDatabase(t *testing.T) {
	st := newStore(t)
	if err := st.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestUserCRUD(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	u := store.User{
		ID:           "u1",
		Email:        "a@b.ru",
		PasswordHash: "hash",
		Role:         store.RoleClient,
		Name:         "Имя",
		CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.UserByEmail(ctx, "A@B.RU")
	if err != nil {
		t.Fatalf("by email: %v", err)
	}
	if got.ID != u.ID || got.Role != u.Role || got.Name != u.Name {
		t.Fatalf("got %+v want %+v", got, u)
	}

	byID, err := st.UserByID(ctx, "u1")
	if err != nil {
		t.Fatalf("by id: %v", err)
	}
	if byID.Email != u.Email {
		t.Fatalf("got email %q", byID.Email)
	}

	if err := st.CreateUser(ctx, u); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate: got %v want ErrConflict", err)
	}

	if _, err := st.UserByID(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing: got %v want ErrNotFound", err)
	}
}

func TestWorkerCRUD(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	w := store.Worker{
		ID:           "w1",
		Name:         "pc-1",
		TokenHash:    "tokenhash",
		Capabilities: "[]",
		Info:         "{}",
		CreatedAt:    time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := st.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.WorkerByName(ctx, "pc-1")
	if err != nil {
		t.Fatalf("by name: %v", err)
	}
	if got.ID != w.ID || got.TokenHash != w.TokenHash {
		t.Fatalf("got %+v", got)
	}
	if err := st.CreateWorker(ctx, w); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate: got %v want ErrConflict", err)
	}
}
