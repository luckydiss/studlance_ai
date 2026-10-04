// Package store defines data access for studlance. The interface is
// implementation-agnostic; the only implementation is SQLite (internal/store/sqlite).
package store

import (
	"context"
	"embed"
	"errors"
	"time"
)

//go:embed migrations/*.sql
var Migrations embed.FS

//go:generate sqlc -f ../../sqlc.yaml generate

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on unique constraint violation.
var ErrConflict = errors.New("conflict")

// Role is a user role.
type Role string

const (
	RoleClient Role = "client"
	RoleAdmin  Role = "admin"
)

// User is an account. Registration is disabled; users are created via CLI.
type User struct {
	ID           string
	Email        string
	PasswordHash string
	Role         Role
	Name         string
	CreatedAt    time.Time
	DisabledAt   *time.Time
}

// Worker is a registered execution host.
type Worker struct {
	ID           string
	Name         string
	TokenHash    string
	Capabilities string
	Info         string
	CreatedAt    time.Time
	LastSeenAt   *time.Time
}

// Store is the data access interface.
type Store interface {
	// Close releases the database connection.
	Close() error

	// Migrate applies all pending migrations.
	Migrate(ctx context.Context) error

	// CreateUser inserts a user. Returns ErrConflict if email exists.
	CreateUser(ctx context.Context, u User) error
	// UserByEmail finds a user by email (case-insensitive).
	UserByEmail(ctx context.Context, email string) (User, error)
	// UserByID finds a user by id.
	UserByID(ctx context.Context, id string) (User, error)

	// CreateWorker inserts a worker. Returns ErrConflict if name or token exists.
	CreateWorker(ctx context.Context, w Worker) error
	// WorkerByName finds a worker by name.
	WorkerByName(ctx context.Context, name string) (Worker, error)

	// Ping checks that the database is reachable.
	Ping(ctx context.Context) error
}
