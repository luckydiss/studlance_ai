package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/luckydiss/studlance_ai/internal/store"
	"github.com/luckydiss/studlance_ai/internal/store/db"
)

// ---------- jobs ----------

func (s *Store) CreateJob(ctx context.Context, j store.Job) error {
	_, err := s.q.CreateJob(ctx, db.CreateJobParams{
		ID:              j.ID,
		UserID:          j.UserID,
		Title:           j.Title,
		Prompt:          j.Prompt,
		Status:          j.Status,
		Stage:           nullString(j.Stage),
		CurrentVersion:  j.CurrentVersion,
		PendingRevision: nullInt64(j.PendingRevision),
		NeedsAttention:  boolToInt(j.NeedsAttention),
		WorkerID:        nullStringPtr(j.WorkerID),
		LeaseEpoch:      j.LeaseEpoch,
		LeaseExpiresAt:  nullMillis(j.LeaseExpiresAt),
		CancelRequested: boolToInt(j.CancelRequested),
		Attempt:         j.Attempt,
		Question:        nullStringPtr(j.Question),
		State:           j.State,
		Error:           nullStringPtr(j.Error),
		CreatedAt:       toMillis(j.CreatedAt),
		UpdatedAt:       toMillis(j.UpdatedAt),
		FinishedAt:      nullMillis(j.FinishedAt),
	})
	return mapErr(err)
}

func (s *Store) JobByID(ctx context.Context, id string) (store.Job, error) {
	j, err := s.q.JobByID(ctx, id)
	if err != nil {
		return store.Job{}, mapErr(err)
	}
	return fromDBJob(j), nil
}

func (s *Store) JobByIDForUser(ctx context.Context, id, userID string) (store.Job, error) {
	j, err := s.q.JobByIDForUser(ctx, db.JobByIDForUserParams{ID: id, UserID: userID})
	if err != nil {
		return store.Job{}, mapErr(err)
	}
	return fromDBJob(j), nil
}

func (s *Store) JobsByUser(ctx context.Context, userID string) ([]store.Job, error) {
	rows, err := s.q.JobsByUser(ctx, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	return mapJobs(rows), nil
}

func (s *Store) AdminJobs(ctx context.Context, f store.JobFilter) ([]store.Job, error) {
	params := db.AdminJobsParams{Attention: boolToInt(f.Attention)}
	if f.Status != "" {
		params.Status = sql.NullString{String: f.Status, Valid: true}
	}
	if f.Query != "" {
		params.Query = sql.NullString{String: "%" + f.Query + "%", Valid: true}
	}
	rows, err := s.q.AdminJobs(ctx, params)
	if err != nil {
		return nil, mapErr(err)
	}
	return mapJobs(rows), nil
}

func (s *Store) UpdateJob(ctx context.Context, j store.Job) error {
	return mapErr(s.q.UpdateJobStatus(ctx, db.UpdateJobStatusParams{
		Status:          j.Status,
		Stage:           nullString(j.Stage),
		CurrentVersion:  j.CurrentVersion,
		PendingRevision: nullInt64(j.PendingRevision),
		NeedsAttention:  boolToInt(j.NeedsAttention),
		Attempt:         j.Attempt,
		Question:        nullStringPtr(j.Question),
		Error:           nullStringPtr(j.Error),
		CancelRequested: boolToInt(j.CancelRequested),
		UpdatedAt:       toMillis(j.UpdatedAt),
		FinishedAt:      nullMillis(j.FinishedAt),
		ID:              j.ID,
	}))
}

func (s *Store) SetJobState(ctx context.Context, id, state string, at time.Time) error {
	return mapErr(s.q.UpdateJobState(ctx, db.UpdateJobStateParams{State: state, UpdatedAt: toMillis(at), ID: id}))
}

func mapJobs(rows []db.Job) []store.Job {
	out := make([]store.Job, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromDBJob(r))
	}
	return out
}

func fromDBJob(j db.Job) store.Job {
	return store.Job{
		ID:              j.ID,
		UserID:          j.UserID,
		Title:           j.Title,
		Prompt:          j.Prompt,
		Status:          j.Status,
		Stage:           fromNullString(j.Stage),
		CurrentVersion:  j.CurrentVersion,
		PendingRevision: fromNullInt64(j.PendingRevision),
		NeedsAttention:  j.NeedsAttention != 0,
		WorkerID:        fromNullStringPtr(j.WorkerID),
		LeaseEpoch:      j.LeaseEpoch,
		LeaseExpiresAt:  fromNullMillis(j.LeaseExpiresAt),
		CancelRequested: j.CancelRequested != 0,
		Attempt:         j.Attempt,
		Question:        fromNullStringPtr(j.Question),
		State:           j.State,
		Error:           fromNullStringPtr(j.Error),
		CreatedAt:       fromMillis(j.CreatedAt),
		UpdatedAt:       fromMillis(j.UpdatedAt),
		FinishedAt:      fromNullMillis(j.FinishedAt),
		StageStartedAt:  fromNullMillis(j.StageStartedAt),
	}
}

// ---------- files ----------

func (s *Store) CreateFile(ctx context.Context, f store.File) error {
	_, err := s.q.CreateFile(ctx, db.CreateFileParams{
		ID:        f.ID,
		JobID:     f.JobID,
		Kind:      f.Kind,
		Version:   f.Version,
		Path:      f.Path,
		BlobKey:   f.BlobKey,
		Size:      f.Size,
		Sha256:    f.SHA256,
		CreatedAt: toMillis(f.CreatedAt),
	})
	return mapErr(err)
}

func (s *Store) FileByBlobKey(ctx context.Context, blobKey string) (store.File, error) {
	f, err := s.q.FileByBlobKey(ctx, blobKey)
	if err != nil {
		return store.File{}, mapErr(err)
	}
	return fromDBFile(f), nil
}

func (s *Store) FilesByJob(ctx context.Context, jobID string) ([]store.File, error) {
	rows, err := s.q.FilesByJob(ctx, jobID)
	if err != nil {
		return nil, mapErr(err)
	}
	return mapFiles(rows), nil
}

func (s *Store) InputFiles(ctx context.Context, jobID string) ([]store.InputFile, error) {
	rows, err := s.q.FilesByJobKind(ctx, db.FilesByJobKindParams{JobID: jobID, Kind: store.FileInput})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.InputFile, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.InputFile{
			ID:        r.ID,
			Path:      r.Path,
			Size:      r.Size,
			SHA256:    r.Sha256,
			Revision:  revisionFromInputPath(r.Path),
			BlobKey:   r.BlobKey,
			CreatedAt: fromMillis(r.CreatedAt),
		})
	}
	return out, nil
}

func (s *Store) InputFileByPath(ctx context.Context, jobID, path string) (store.InputFile, error) {
	f, err := s.q.FileByJobKindPath(ctx, db.FileByJobKindPathParams{JobID: jobID, Kind: store.FileInput, Path: path})
	if err != nil {
		return store.InputFile{}, mapErr(err)
	}
	return store.InputFile{
		ID:        f.ID,
		Path:      f.Path,
		Size:      f.Size,
		SHA256:    f.Sha256,
		Revision:  revisionFromInputPath(f.Path),
		BlobKey:   f.BlobKey,
		CreatedAt: fromMillis(f.CreatedAt),
	}, nil
}

func (s *Store) DeleteInputFileByPath(ctx context.Context, jobID, path string) error {
	return mapErr(s.q.DeleteFileByJobKindPath(ctx, db.DeleteFileByJobKindPathParams{JobID: jobID, Kind: store.FileInput, Path: path}))
}

func (s *Store) SumInputBytes(ctx context.Context, jobID string) (int64, error) {
	v, err := s.q.SumInputBytes(ctx, jobID)
	if err != nil {
		return 0, mapErr(err)
	}
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case float64:
		return int64(n), nil
	default:
		return 0, nil
	}
}

func mapFiles(rows []db.File) []store.File {
	out := make([]store.File, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromDBFile(r))
	}
	return out
}

func fromDBFile(f db.File) store.File {
	return store.File{
		ID:        f.ID,
		JobID:     f.JobID,
		Kind:      f.Kind,
		Version:   f.Version,
		Path:      f.Path,
		BlobKey:   f.BlobKey,
		Size:      f.Size,
		SHA256:    f.Sha256,
		CreatedAt: fromMillis(f.CreatedAt),
	}
}

// ---------- documents & pages ----------

func (s *Store) ReplaceDocuments(ctx context.Context, jobID, snapshot string, version int64, docs []store.DocumentWithPages) error {
	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)

	if err := q.DeleteDocumentsBySnapshot(ctx, db.DeleteDocumentsBySnapshotParams{JobID: jobID, Snapshot: snapshot, Version: version}); err != nil {
		return mapErr(err)
	}
	for _, d := range docs {
		if _, err := q.CreateDocument(ctx, db.CreateDocumentParams{
			ID:          d.Document.ID,
			JobID:       jobID,
			Snapshot:    snapshot,
			Version:     version,
			Idx:         d.Document.Idx,
			Title:       d.Document.Title,
			Kind:        d.Document.Kind,
			FilePath:    d.Document.FilePath,
			PreviewPath: nullStringPtr(d.Document.PreviewPath),
			PageCount:   d.Document.PageCount,
		}); err != nil {
			return mapErr(err)
		}
		for _, p := range d.Pages {
			if err := q.CreatePage(ctx, db.CreatePageParams{
				DocumentID:   d.Document.ID,
				Page:         p.Page,
				ImageKey:     p.ImageKey,
				ThumbKey:     p.ThumbKey,
				Width:        p.Width,
				Height:       p.Height,
				ChangedBoxes: p.ChangedBoxes,
			}); err != nil {
				return mapErr(err)
			}
		}
	}
	return tx.Commit()
}

func (s *Store) DocumentsBySnapshot(ctx context.Context, jobID, snapshot string, version int64) ([]store.Document, error) {
	rows, err := s.q.DocumentsBySnapshot(ctx, db.DocumentsBySnapshotParams{JobID: jobID, Snapshot: snapshot, Version: version})
	if err != nil {
		return nil, mapErr(err)
	}
	return mapDocuments(rows), nil
}

func (s *Store) DocumentByID(ctx context.Context, id string) (store.Document, error) {
	d, err := s.q.DocumentByID(ctx, id)
	if err != nil {
		return store.Document{}, mapErr(err)
	}
	return fromDBDocument(d), nil
}

func (s *Store) PagesByDocument(ctx context.Context, documentID string) ([]store.Page, error) {
	rows, err := s.q.PagesByDocument(ctx, documentID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.Page, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromDBPage(r))
	}
	return out, nil
}

func mapDocuments(rows []db.Document) []store.Document {
	out := make([]store.Document, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromDBDocument(r))
	}
	return out
}

func fromDBDocument(d db.Document) store.Document {
	return store.Document{
		ID:          d.ID,
		JobID:       d.JobID,
		Snapshot:    d.Snapshot,
		Version:     d.Version,
		Idx:         d.Idx,
		Title:       d.Title,
		Kind:        d.Kind,
		FilePath:    d.FilePath,
		PreviewPath: fromNullStringPtr(d.PreviewPath),
		PageCount:   d.PageCount,
	}
}

func fromDBPage(p db.Page) store.Page {
	return store.Page{
		DocumentID:   p.DocumentID,
		Page:         p.Page,
		ImageKey:     p.ImageKey,
		ThumbKey:     p.ThumbKey,
		Width:        p.Width,
		Height:       p.Height,
		ChangedBoxes: p.ChangedBoxes,
	}
}

// ---------- revisions ----------

func (s *Store) CreateRevision(ctx context.Context, rev store.Revision, remarks []store.Remark) error {
	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	if _, err := q.CreateRevision(ctx, db.CreateRevisionParams{
		ID:          rev.ID,
		JobID:       rev.JobID,
		Version:     rev.Version,
		Comment:     rev.Comment,
		CreatedAt:   toMillis(rev.CreatedAt),
		CompletedAt: nullMillis(rev.CompletedAt),
	}); err != nil {
		return mapErr(err)
	}
	for _, r := range remarks {
		if _, err := q.CreateRevisionRemark(ctx, db.CreateRevisionRemarkParams{
			ID:         r.ID,
			RevisionID: r.RevisionID,
			Idx:        r.Idx,
			DocumentID: r.DocumentID,
			Page:       r.Page,
			X:          r.X,
			Y:          r.Y,
			W:          r.W,
			H:          r.H,
			Text:       r.Text,
		}); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit()
}

func (s *Store) RevisionsByJob(ctx context.Context, jobID string) ([]store.Revision, error) {
	rows, err := s.q.RevisionsByJob(ctx, jobID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.Revision, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromDBRevision(r))
	}
	return out, nil
}

func (s *Store) RemarksByRevision(ctx context.Context, revisionID string) ([]store.Remark, error) {
	rows, err := s.q.RemarksByRevision(ctx, revisionID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.Remark, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.Remark{
			ID: r.ID, RevisionID: r.RevisionID, Idx: r.Idx, DocumentID: r.DocumentID,
			Page: r.Page, X: r.X, Y: r.Y, W: r.W, H: r.H, Text: r.Text,
		})
	}
	return out, nil
}

func fromDBRevision(r db.Revision) store.Revision {
	return store.Revision{
		ID:          r.ID,
		JobID:       r.JobID,
		Version:     r.Version,
		Comment:     r.Comment,
		CreatedAt:   fromMillis(r.CreatedAt),
		CompletedAt: fromNullMillis(r.CompletedAt),
	}
}

// ---------- events & notes ----------

func (s *Store) CreateEvent(ctx context.Context, e store.Event) error {
	_, err := s.q.CreateEvent(ctx, db.CreateEventParams{
		ID:              e.ID,
		JobID:           e.JobID,
		Ts:              toMillis(e.Ts),
		Kind:            e.Kind,
		VisibleToClient: boolToInt(e.VisibleToClient),
		Data:            e.Data,
	})
	return mapErr(err)
}

func (s *Store) EventsByJob(ctx context.Context, jobID string) ([]store.Event, error) {
	rows, err := s.q.EventsByJob(ctx, jobID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.Event{
			ID: r.ID, JobID: r.JobID, Ts: fromMillis(r.Ts), Kind: r.Kind,
			VisibleToClient: r.VisibleToClient != 0, Data: r.Data,
		})
	}
	return out, nil
}

func (s *Store) CreateNote(ctx context.Context, n store.Note) error {
	_, err := s.q.CreateNote(ctx, db.CreateNoteParams{
		ID:        n.ID,
		JobID:     n.JobID,
		AuthorID:  n.AuthorID,
		Text:      n.Text,
		CreatedAt: toMillis(n.CreatedAt),
	})
	return mapErr(err)
}

func (s *Store) NotesByJob(ctx context.Context, jobID string) ([]store.Note, error) {
	rows, err := s.q.NotesByJob(ctx, jobID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.Note, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.Note{ID: r.ID, JobID: r.JobID, AuthorID: r.AuthorID, Text: r.Text, CreatedAt: fromMillis(r.CreatedAt)})
	}
	return out, nil
}

// ---------- sessions & login attempts ----------

func (s *Store) CreateSession(ctx context.Context, ss store.Session) error {
	return mapErr(s.q.CreateSession(ctx, db.CreateSessionParams{
		IDHash:     ss.IDHash,
		UserID:     ss.UserID,
		CreatedAt:  toMillis(ss.CreatedAt),
		ExpiresAt:  toMillis(ss.ExpiresAt),
		LastSeenAt: toMillis(ss.LastSeenAt),
		UserAgent:  ss.UserAgent,
		Ip:         ss.IP,
	}))
}

func (s *Store) SessionByIDHash(ctx context.Context, idHash string) (store.Session, error) {
	r, err := s.q.SessionByIDHash(ctx, idHash)
	if err != nil {
		return store.Session{}, mapErr(err)
	}
	return store.Session{
		IDHash: r.IDHash, UserID: r.UserID, CreatedAt: fromMillis(r.CreatedAt),
		ExpiresAt: fromMillis(r.ExpiresAt), LastSeenAt: fromMillis(r.LastSeenAt),
		UserAgent: r.UserAgent, IP: r.Ip,
	}, nil
}

func (s *Store) TouchSession(ctx context.Context, idHash string, at time.Time) error {
	return mapErr(s.q.TouchSession(ctx, db.TouchSessionParams{LastSeenAt: toMillis(at), IDHash: idHash}))
}

func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	return mapErr(s.q.DeleteSession(ctx, idHash))
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID string) error {
	return mapErr(s.q.DeleteUserSessions(ctx, userID))
}

func (s *Store) CountLoginAttempts(ctx context.Context, key string, since time.Time) (int64, error) {
	return s.q.CountLoginAttempts(ctx, db.CountLoginAttemptsParams{Key: key, At: toMillis(since)})
}

func (s *Store) CreateLoginAttempt(ctx context.Context, key string, at time.Time) error {
	return mapErr(s.q.CreateLoginAttempt(ctx, db.CreateLoginAttemptParams{Key: key, At: toMillis(at)}))
}

func (s *Store) ClearLoginAttempts(ctx context.Context, key string) error {
	return mapErr(s.q.DeleteLoginAttempts(ctx, key))
}

// ---------- clients ----------

func (s *Store) Clients(ctx context.Context) ([]store.ClientAccount, error) {
	rows, err := s.q.CountUserJobs(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	byID := make(map[string]store.ClientAccount, len(rows))
	order := make([]string, 0, len(rows))
	for _, r := range rows {
		byID[r.UserID] = store.ClientAccount{
			ID:        r.UserID,
			JobsTotal: r.JobsTotal,
			LastJobAt: interfaceToTime(r.LastJobAt),
		}
		order = append(order, r.UserID)
	}
	clients, err := s.q.ListClients(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.ClientAccount, 0, len(clients))
	for _, c := range clients {
		acc := byID[c.ID]
		acc.ID = c.ID
		acc.Email = c.Email
		acc.Name = c.Name
		out = append(out, acc)
	}
	_ = order
	return out, nil
}

// ---------- workers ----------

func (s *Store) WorkerByTokenHash(ctx context.Context, tokenHash string) (store.Worker, error) {
	w, err := s.q.WorkerByTokenHash(ctx, tokenHash)
	if err != nil {
		return store.Worker{}, mapErr(err)
	}
	return fromDBWorker(w), nil
}

func (s *Store) WorkerByID(ctx context.Context, id string) (store.Worker, error) {
	w, err := s.q.WorkerByID(ctx, id)
	if err != nil {
		return store.Worker{}, mapErr(err)
	}
	return fromDBWorker(w), nil
}

func (s *Store) TouchWorker(ctx context.Context, id string, at time.Time) error {
	return mapErr(s.q.TouchWorker(ctx, db.TouchWorkerParams{LastSeenAt: nullMillis(&at), ID: id}))
}

func (s *Store) ListWorkers(ctx context.Context) ([]store.Worker, error) {
	rows, err := s.q.ListWorkers(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.Worker, 0, len(rows))
	for _, w := range rows {
		out = append(out, fromDBWorker(w))
	}
	return out, nil
}

// ---------- agent runs & trace ----------

func (s *Store) CreateAgentRun(ctx context.Context, r store.AgentRun) error {
	_, err := s.q.CreateAgentRun(ctx, db.CreateAgentRunParams{
		ID:           r.ID,
		JobID:        r.JobID,
		Version:      r.Version,
		Agent:        r.Agent,
		Stage:        r.Stage,
		Attempt:      r.Attempt,
		SessionID:    nullStringPtr(r.SessionID),
		StartedAt:    toMillis(r.StartedAt),
		FinishedAt:   nullMillis(r.FinishedAt),
		ExitCode:     nullInt64(r.ExitCode),
		Outcome:      nullStringPtr(r.Outcome),
		InputTokens:  r.InputTokens,
		OutputTokens: r.OutputTokens,
		CostUsd:      r.CostUSD,
		RawLogKey:    nullStringPtr(r.RawLogKey),
		Error:        nullStringPtr(r.Error),
	})
	return mapErr(err)
}

func (s *Store) AgentRunByID(ctx context.Context, id, jobID string) (store.AgentRun, error) {
	r, err := s.q.AgentRunByID(ctx, db.AgentRunByIDParams{ID: id, JobID: jobID})
	if err != nil {
		return store.AgentRun{}, mapErr(err)
	}
	return fromDBAgentRun(r), nil
}

func (s *Store) AgentRunsByJob(ctx context.Context, jobID string) ([]store.AgentRun, error) {
	rows, err := s.q.AgentRunsByJob(ctx, jobID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.AgentRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromDBAgentRun(r))
	}
	return out, nil
}

func (s *Store) PatchAgentRun(ctx context.Context, id, jobID string, p store.AgentRunPatch) error {
	return mapErr(s.q.PatchAgentRun(ctx, db.PatchAgentRunParams{
		SessionID:    nullStringPtr(p.SessionID),
		FinishedAt:   nullMillis(p.FinishedAt),
		ExitCode:     nullInt64(p.ExitCode),
		Outcome:      nullStringPtr(p.Outcome),
		InputTokens:  nullInt64(p.InputTokens),
		OutputTokens: nullInt64(p.OutputTokens),
		CostUsd:      nullFloat64(p.CostUSD),
		Error:        nullStringPtr(p.Error),
		ID:           id,
		JobID:        jobID,
	}))
}

func (s *Store) AppendTraceSteps(ctx context.Context, steps []store.TraceStep) error {
	for _, st := range steps {
		if err := s.q.CreateTraceStep(ctx, db.CreateTraceStepParams{
			AgentRunID: st.AgentRunID,
			Seq:        st.Seq,
			Ts:         toMillis(st.Ts),
			Type:       st.Type,
			Summary:    st.Summary,
			Payload:    st.Payload,
		}); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func (s *Store) TraceStepsByRun(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.TraceStep, error) {
	rows, err := s.q.TraceStepsByRun(ctx, db.TraceStepsByRunParams{AgentRunID: runID, Seq: afterSeq, Limit: int64(limit)})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.TraceStep, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.TraceStep{
			AgentRunID: r.AgentRunID, Seq: r.Seq, Ts: fromMillis(r.Ts),
			Type: r.Type, Summary: r.Summary, Payload: r.Payload,
		})
	}
	return out, nil
}

func (s *Store) SaveRawLogKey(ctx context.Context, id, jobID, key string) error {
	return mapErr(s.q.SaveRawLogKey(ctx, db.SaveRawLogKeyParams{RawLogKey: sql.NullString{String: key, Valid: true}, ID: id, JobID: jobID}))
}

func fromDBAgentRun(r db.AgentRun) store.AgentRun {
	return store.AgentRun{
		ID:           r.ID,
		JobID:        r.JobID,
		Version:      r.Version,
		Agent:        r.Agent,
		Stage:        r.Stage,
		Attempt:      r.Attempt,
		SessionID:    fromNullStringPtr(r.SessionID),
		StartedAt:    fromMillis(r.StartedAt),
		FinishedAt:   fromNullMillis(r.FinishedAt),
		ExitCode:     fromNullInt64(r.ExitCode),
		Outcome:      fromNullStringPtr(r.Outcome),
		InputTokens:  r.InputTokens,
		OutputTokens: r.OutputTokens,
		CostUSD:      r.CostUsd,
		RawLogKey:    fromNullStringPtr(r.RawLogKey),
		Error:        fromNullStringPtr(r.Error),
	}
}

// ---------- helpers ----------

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullStringPtr(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

func fromNullString(n sql.NullString) string {
	if !n.Valid {
		return ""
	}
	return n.String
}

func fromNullStringPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	s := n.String
	return &s
}

func nullInt64(n *int64) sql.NullInt64 {
	if n == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *n, Valid: true}
}

func fromNullInt64(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func nullFloat64(n *float64) sql.NullFloat64 {
	if n == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *n, Valid: true}
}

// interfaceToTime converts a sqlc interface{} column (e.g. MAX) to *time.Time.
func interfaceToTime(v interface{}) *time.Time {
	switch t := v.(type) {
	case nil:
		return nil
	case int64:
		if t == 0 {
			return nil
		}
		ts := fromMillis(t)
		return &ts
	case time.Time:
		ts := t.UTC()
		return &ts
	default:
		return nil
	}
}

// revisionFromInputPath returns the revision number encoded in an input path:
// 0 for "input/<path>", n for "input/revision-<n>/<path>".
func revisionFromInputPath(path string) int64 {
	const prefix = "input/revision-"
	if len(path) > len(prefix) && path[:len(prefix)] == prefix {
		rest := path[len(prefix):]
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i > 0 && i < len(rest) && rest[i] == '/' {
			var n int64
			for j := 0; j < i; j++ {
				n = n*10 + int64(rest[j]-'0')
			}
			return n
		}
	}
	return 0
}
