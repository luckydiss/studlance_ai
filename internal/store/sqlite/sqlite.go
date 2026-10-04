// Package sqlite implements store.Store on top of modernc.org/sqlite (no CGO).
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/luckydiss/studlance_ai/internal/store"
	"github.com/luckydiss/studlance_ai/internal/store/db"
	"github.com/pressly/goose/v3"

	_ "modernc.org/sqlite"
)

// Store is a SQLite-backed store.Store.
type Store struct {
	sql *sql.DB
	q   *db.Queries
}

// Open opens (and creates if needed) the SQLite database at path.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return &Store{sql: sqlDB, q: db.New(sqlDB)}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.sql.Close() }

// Migrate applies embedded goose migrations.
func (s *Store) Migrate(ctx context.Context) error {
	goose.SetBaseFS(store.Migrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, s.sql, "migrations"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Ping checks connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.sql.PingContext(ctx) }

// CreateUser inserts a user.
func (s *Store) CreateUser(ctx context.Context, u store.User) error {
	_, err := s.q.CreateUser(ctx, db.CreateUserParams{
		ID:           u.ID,
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
		Role:         string(u.Role),
		Name:         u.Name,
		CreatedAt:    toMillis(u.CreatedAt),
		DisabledAt:   nullMillis(u.DisabledAt),
	})
	return mapErr(err)
}

// UserByEmail finds a user by email.
func (s *Store) UserByEmail(ctx context.Context, email string) (store.User, error) {
	u, err := s.q.UserByEmail(ctx, email)
	if err != nil {
		return store.User{}, mapErr(err)
	}
	return fromDBUser(u), nil
}

// UserByID finds a user by id.
func (s *Store) UserByID(ctx context.Context, id string) (store.User, error) {
	u, err := s.q.UserByID(ctx, id)
	if err != nil {
		return store.User{}, mapErr(err)
	}
	return fromDBUser(u), nil
}

// CreateWorker inserts a worker.
func (s *Store) CreateWorker(ctx context.Context, w store.Worker) error {
	_, err := s.q.CreateWorker(ctx, db.CreateWorkerParams{
		ID:           w.ID,
		Name:         w.Name,
		TokenHash:    w.TokenHash,
		Capabilities: w.Capabilities,
		Info:         w.Info,
		CreatedAt:    toMillis(w.CreatedAt),
		LastSeenAt:   nullMillis(w.LastSeenAt),
	})
	return mapErr(err)
}

// WorkerByName finds a worker by name.
func (s *Store) WorkerByName(ctx context.Context, name string) (store.Worker, error) {
	w, err := s.q.WorkerByName(ctx, name)
	if err != nil {
		return store.Worker{}, mapErr(err)
	}
	return fromDBWorker(w), nil
}

func fromDBUser(u db.User) store.User {
	return store.User{
		ID:           u.ID,
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
		Role:         store.Role(u.Role),
		Name:         u.Name,
		CreatedAt:    fromMillis(u.CreatedAt),
		DisabledAt:   fromNullMillis(u.DisabledAt),
	}
}

func fromDBWorker(w db.Worker) store.Worker {
	return store.Worker{
		ID:           w.ID,
		Name:         w.Name,
		TokenHash:    w.TokenHash,
		Capabilities: w.Capabilities,
		Info:         w.Info,
		CreatedAt:    fromMillis(w.CreatedAt),
		LastSeenAt:   fromNullMillis(w.LastSeenAt),
	}
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if isUniqueViolation(err) {
		return store.ErrConflict
	}
	return err
}

func isUniqueViolation(err error) bool {
	// modernc.org/sqlite returns a *sqlite.Error with code 19 (SQLITE_CONSTRAINT).
	type coder interface{ Code() int }
	var c coder
	if errors.As(err, &c) {
		return c.Code() == 19 || c.Code() == 1555 || c.Code() == 2067
	}
	return false
}

func toMillis(t time.Time) int64 { return t.UTC().UnixMilli() }

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func nullMillis(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UTC().UnixMilli(), Valid: true}
}

func fromNullMillis(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.UnixMilli(n.Int64).UTC()
	return &t
}
