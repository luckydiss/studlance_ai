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
	// WorkerByID finds a worker by id.
	WorkerByID(ctx context.Context, id string) (Worker, error)
	// WorkerByTokenHash finds a worker by its token sha256.
	WorkerByTokenHash(ctx context.Context, tokenHash string) (Worker, error)
	// TouchWorker updates last_seen_at.
	TouchWorker(ctx context.Context, id string, at time.Time) error
	// ListWorkers returns all workers ordered by name.
	ListWorkers(ctx context.Context) ([]Worker, error)

	// CreateJob inserts a job.
	CreateJob(ctx context.Context, j Job) error
	// JobByID finds a job by id.
	JobByID(ctx context.Context, id string) (Job, error)
	// JobByIDForUser finds a job owned by user (ErrNotFound otherwise).
	JobByIDForUser(ctx context.Context, id, userID string) (Job, error)
	// JobsByUser lists the user's jobs, newest first.
	JobsByUser(ctx context.Context, userID string) ([]Job, error)
	// AdminJobs lists jobs with optional filters.
	AdminJobs(ctx context.Context, f JobFilter) ([]Job, error)
	// UpdateJob persists the mutable job fields.
	UpdateJob(ctx context.Context, j Job) error
	// SetJobState updates only the JSON state column.
	SetJobState(ctx context.Context, id, state string, at time.Time) error

	// CreateFile inserts a file record.
	CreateFile(ctx context.Context, f File) error
	// FileByBlobKey finds a file by its blob key.
	FileByBlobKey(ctx context.Context, blobKey string) (File, error)
	// FilesByJob returns all files of a job.
	FilesByJob(ctx context.Context, jobID string) ([]File, error)
	// InputFiles returns input files (kind=input) of a job.
	InputFiles(ctx context.Context, jobID string) ([]InputFile, error)
	// InputFileByPath finds an input file by job and path.
	InputFileByPath(ctx context.Context, jobID, path string) (InputFile, error)
	// DeleteInputFileByPath removes an input file.
	DeleteInputFileByPath(ctx context.Context, jobID, path string) error
	// SumInputBytes returns the total size of input files.
	SumInputBytes(ctx context.Context, jobID string) (int64, error)

	// ReplaceDocuments atomically replaces documents (and their pages) of a snapshot.
	ReplaceDocuments(ctx context.Context, jobID, snapshot string, version int64, docs []DocumentWithPages) error
	// DocumentsBySnapshot returns documents of a snapshot snapshot version ordered by idx.
	DocumentsBySnapshot(ctx context.Context, jobID, snapshot string, version int64) ([]Document, error)
	// DocumentByID finds a document by id.
	DocumentByID(ctx context.Context, id string) (Document, error)
	// PagesByDocument returns pages of a document.
	PagesByDocument(ctx context.Context, documentID string) ([]Page, error)

	// CreateRevision inserts a revision with its remarks.
	CreateRevision(ctx context.Context, rev Revision, remarks []Remark) error
	// RevisionsByJob returns revisions ordered by version.
	RevisionsByJob(ctx context.Context, jobID string) ([]Revision, error)
	// RemarksByRevision returns remarks ordered by idx.
	RemarksByRevision(ctx context.Context, revisionID string) ([]Remark, error)

	// CreateEvent inserts an event.
	CreateEvent(ctx context.Context, e Event) error
	// EventsByJob returns events ordered by ts.
	EventsByJob(ctx context.Context, jobID string) ([]Event, error)

	// CreateNote inserts an admin note.
	CreateNote(ctx context.Context, n Note) error
	// NotesByJob returns notes ordered by created_at.
	NotesByJob(ctx context.Context, jobID string) ([]Note, error)

	// CreateSession inserts a session.
	CreateSession(ctx context.Context, s Session) error
	// SessionByIDHash finds a session by token hash.
	SessionByIDHash(ctx context.Context, idHash string) (Session, error)
	// TouchSession updates last_seen_at.
	TouchSession(ctx context.Context, idHash string, at time.Time) error
	// DeleteSession removes a session.
	DeleteSession(ctx context.Context, idHash string) error
	// DeleteUserSessions removes all sessions of a user.
	DeleteUserSessions(ctx context.Context, userID string) error

	// CountLoginAttempts counts recent failed attempts for a key since a time.
	CountLoginAttempts(ctx context.Context, key string, since time.Time) (int64, error)
	// CreateLoginAttempt records a failed login attempt.
	CreateLoginAttempt(ctx context.Context, key string, at time.Time) error
	// ClearLoginAttempts removes attempts for a key.
	ClearLoginAttempts(ctx context.Context, key string) error

	// Clients returns client accounts with job counts.
	Clients(ctx context.Context) ([]ClientAccount, error)

	// CreateAgentRun inserts an agent run.
	CreateAgentRun(ctx context.Context, r AgentRun) error
	// AgentRunByID finds a run within a job.
	AgentRunByID(ctx context.Context, id, jobID string) (AgentRun, error)
	// AgentRunsByJob returns runs ordered by started_at.
	AgentRunsByJob(ctx context.Context, jobID string) ([]AgentRun, error)
	// PatchAgentRun updates optional run fields.
	PatchAgentRun(ctx context.Context, id, jobID string, p AgentRunPatch) error
	// AppendTraceSteps inserts steps, ignoring duplicates.
	AppendTraceSteps(ctx context.Context, steps []TraceStep) error
	// TraceStepsByRun returns steps with seq > afterSeq, up to limit.
	TraceStepsByRun(ctx context.Context, runID string, afterSeq int64, limit int) ([]TraceStep, error)
	// SaveRawLogKey sets the raw log blob key of a run.
	SaveRawLogKey(ctx context.Context, id, jobID, key string) error

	// Ping checks that the database is reachable.
	Ping(ctx context.Context) error
}

// File is a stored blob reference tied to a job.
type File struct {
	ID        string
	JobID     string
	Kind      string
	Version   int64
	Path      string
	BlobKey   string
	Size      int64
	SHA256    string
	CreatedAt time.Time
}

// DocumentWithPages is a document plus its rendered pages, used when committing
// a draft or version snapshot.
type DocumentWithPages struct {
	Document Document
	Pages    []Page
}
