package store

import "time"

// Job status values.
const (
	StatusUploading  = "uploading"
	StatusQueued     = "queued"
	StatusRunning    = "running"
	StatusNeedsInput = "needs_input"
	StatusDone       = "done"
	StatusFailed     = "failed"
	StatusCanceled   = "canceled"
)

// Job stage values.
const (
	StageDraft  = "draft"
	StageVerify = "verify"
	StageRevise = "revise"
)

// File kinds.
const (
	FileInput   = "input"
	FileDraft   = "draft"
	FileVersion = "version"
	FileLog     = "log"
	FilePage    = "page"
	FileThumb   = "thumb"
	FileCrop    = "crop"
)

// Snapshot kinds.
const (
	SnapshotDraft   = "draft"
	SnapshotVersion = "version"
)

// Job is one student order with all its versions.
type Job struct {
	ID              string
	UserID          string
	Title           string
	Prompt          string
	Status          string
	Stage           string
	CurrentVersion  int64
	PendingRevision *int64
	NeedsAttention  bool
	WorkerID        *string
	LeaseEpoch      int64
	LeaseExpiresAt  *time.Time
	CancelRequested bool
	Attempt         int64
	Question        *string
	State           string
	Error           *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      *time.Time
}

// InputFile is an uploaded source file.
type InputFile struct {
	ID        string
	Path      string
	Size      int64
	SHA256    string
	Revision  int64
	BlobKey   string
	CreatedAt time.Time
}

// Document is one component of a snapshot (draft or version).
type Document struct {
	ID          string
	JobID       string
	Snapshot    string
	Version     int64
	Idx         int64
	Title       string
	Kind        string
	FilePath    string
	PreviewPath *string
	PageCount   int64
}

// Page is a rendered page of a document.
type Page struct {
	DocumentID   string
	Page         int64
	ImageKey     string
	ThumbKey     string
	Width        int64
	Height       int64
	ChangedBoxes string
}

// Revision is a client-requested rework.
type Revision struct {
	ID          string
	JobID       string
	Version     int64
	Comment     string
	CreatedAt   time.Time
	CompletedAt *time.Time
}

// Remark is one marked region on a page inside a revision.
type Remark struct {
	ID         string
	RevisionID string
	Idx        int64
	DocumentID string
	Page       int64
	X          float64
	Y          float64
	W          float64
	H          float64
	Text       string
}

// Event is a job lifecycle event.
type Event struct {
	ID              string
	JobID           string
	Ts              time.Time
	Kind            string
	VisibleToClient bool
	Data            string
}

// Note is an admin note on a job.
type Note struct {
	ID        string
	JobID     string
	AuthorID  string
	Text      string
	CreatedAt time.Time
}

// Session is a server-side login session (token stored hashed).
type Session struct {
	IDHash     string
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
	IP         string
}

// AgentRun is one agent invocation.
type AgentRun struct {
	ID           string
	JobID        string
	Version      int64
	Agent        string
	Stage        string
	Attempt      int64
	SessionID    *string
	StartedAt    time.Time
	FinishedAt   *time.Time
	ExitCode     *int64
	Outcome      *string
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
	RawLogKey    *string
	Error        *string
}

// TraceStep is one parsed step of an agent run.
type TraceStep struct {
	AgentRunID string
	Seq        int64
	Ts         time.Time
	Type       string
	Summary    string
	Payload    string
}

// ClientAccount is an admin view of a client and their activity.
type ClientAccount struct {
	ID        string
	Email     string
	Name      string
	JobsTotal int64
	LastJobAt *time.Time
}

// WorkerStatus is an admin view of a worker.
type WorkerStatus struct {
	Worker
	Online     bool
	CurrentJob *string
}

// JobFilter filters the admin job list.
type JobFilter struct {
	Status    string
	Attention bool
	Query     string
}

// AgentRunPatch carries optional fields for updating an agent run.
type AgentRunPatch struct {
	SessionID    *string
	FinishedAt   *time.Time
	ExitCode     *int64
	Outcome      *string
	InputTokens  *int64
	OutputTokens *int64
	CostUSD      *float64
	Error        *string
}
