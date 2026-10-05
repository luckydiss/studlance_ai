package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/luckydiss/studlance_ai/internal/jobs"
	"github.com/luckydiss/studlance_ai/internal/store"
)

// adminJobSummary builds the admin list view.
func (s *Server) adminJobSummary(ctx context.Context, j store.Job) AdminJobSummary {
	cs := jobs.ClientStatusFor(j)
	ref := ClientRef{Id: j.UserID}
	if u, err := s.store.UserByID(ctx, j.UserID); err == nil {
		ref.Email = u.Email
		ref.Name = u.Name
	}
	sum := AdminJobSummary{
		Id:             j.ID,
		Title:          j.Title,
		Client:         ref,
		Status:         j.Status,
		ClientStatus:   string(cs),
		CurrentVersion: int(j.CurrentVersion),
		NeedsAttention: j.NeedsAttention,
		Attempt:        int(j.Attempt),
		CreatedAt:      j.CreatedAt,
		UpdatedAt:      j.UpdatedAt,
	}
	if j.Stage != "" {
		sum.Stage = &j.Stage
	}
	if j.PendingRevision != nil {
		v := int(*j.PendingRevision)
		sum.PendingRevision = &v
	}
	if j.WorkerID != nil {
		if w, err := s.store.WorkerByID(ctx, *j.WorkerID); err == nil {
			name := w.Name
			sum.Worker = &name
		}
	}
	return sum
}

// adminJobDetail builds the full admin view.
func (s *Server) adminJobDetail(ctx context.Context, j store.Job) (AdminJobDetail, error) {
	detail := AdminJobDetail{
		Id:               j.ID,
		Title:            j.Title,
		Client:           ClientRef{Id: j.UserID},
		Status:           j.Status,
		ClientStatus:     string(jobs.ClientStatusFor(j)),
		CurrentVersion:   int(j.CurrentVersion),
		NeedsAttention:   j.NeedsAttention,
		Attempt:          int(j.Attempt),
		CreatedAt:        j.CreatedAt,
		UpdatedAt:        j.UpdatedAt,
		Prompt:           j.Prompt,
		Question:         j.Question,
		StatusSteps:      s.statusSteps(ctx, j),
		InputFiles:       []InputFile{},
		Versions:         []Version{},
		PlannedDocuments: []PlannedDocument{},
		Revisions:        []Revision{},
		AgentRuns:        []AgentRun{},
		Events:           []Event{},
		Notes:            []JobNote{},
		LeaseEpoch:       int(j.LeaseEpoch),
	}
	if u, err := s.store.UserByID(ctx, j.UserID); err == nil {
		detail.Client = ClientRef{Id: u.ID, Email: u.Email, Name: u.Name}
	}
	if j.Stage != "" {
		detail.Stage = &j.Stage
	}
	if j.PendingRevision != nil {
		v := int(*j.PendingRevision)
		detail.PendingRevision = &v
	}
	if j.WorkerID != nil {
		if w, err := s.store.WorkerByID(ctx, *j.WorkerID); err == nil {
			name := w.Name
			detail.Worker = &name
		}
	}
	if j.Error != nil {
		detail.Error = *j.Error
	}
	if j.LeaseExpiresAt != nil {
		detail.LeaseExpiresAt = j.LeaseExpiresAt
	}
	detail.State = map[string]interface{}{}
	if j.State != "" {
		_ = json.Unmarshal([]byte(j.State), &detail.State)
	}

	input, err := s.store.InputFiles(ctx, j.ID)
	if err != nil {
		return AdminJobDetail{}, err
	}
	for _, f := range input {
		detail.InputFiles = append(detail.InputFiles, InputFile{Path: f.Path, Size: int(f.Size)})
	}

	versions, err := s.versionViews(ctx, j, true)
	if err != nil {
		return AdminJobDetail{}, err
	}
	detail.Versions = versions

	revs, err := s.revisionViews(ctx, j.ID)
	if err != nil {
		return AdminJobDetail{}, err
	}
	detail.Revisions = revs

	if j.CurrentVersion == 0 {
		docs, dErr := s.store.DocumentsBySnapshot(ctx, j.ID, store.SnapshotDraft, 0)
		if dErr == nil {
			for _, d := range docs {
				detail.PlannedDocuments = append(detail.PlannedDocuments, PlannedDocument{Title: d.Title, Kind: d.Kind})
			}
		}
	}

	draftDocs, _ := s.store.DocumentsBySnapshot(ctx, j.ID, store.SnapshotDraft, 0)
	if len(draftDocs) > 0 {
		detail.Draft = &DraftSnapshot{Documents: s.documentViews(ctx, j.ID, draftDocs, true)}
	}

	detail.Verification = s.latestVerification(ctx, j)

	runs, err := s.store.AgentRunsByJob(ctx, j.ID)
	if err != nil {
		return AdminJobDetail{}, err
	}
	for _, r := range runs {
		detail.AgentRuns = append(detail.AgentRuns, agentRunView(r))
	}

	events, err := s.store.EventsByJob(ctx, j.ID)
	if err != nil {
		return AdminJobDetail{}, err
	}
	for _, e := range events {
		detail.Events = append(detail.Events, Event{Ts: e.Ts, Kind: e.Kind, Data: eventData(e.Data)})
	}

	notes, err := s.store.NotesByJob(ctx, j.ID)
	if err != nil {
		return AdminJobDetail{}, err
	}
	for _, n := range notes {
		detail.Notes = append(detail.Notes, JobNote{Id: n.ID, Author: n.AuthorID, Text: n.Text, CreatedAt: n.CreatedAt})
	}
	return detail, nil
}

func agentRunView(r store.AgentRun) AgentRun {
	v := AgentRun{
		Id:        r.ID,
		Agent:     r.Agent,
		Stage:     r.Stage,
		Version:   int(r.Version),
		Attempt:   int(r.Attempt),
		StartedAt: r.StartedAt,
	}
	if r.FinishedAt != nil {
		v.FinishedAt = r.FinishedAt
	}
	if r.Outcome != nil {
		v.Outcome = r.Outcome
	}
	if r.Error != nil {
		v.Error = r.Error
	}
	in := int(r.InputTokens)
	out := int(r.OutputTokens)
	cost := float32(r.CostUSD)
	v.InputTokens = &in
	v.OutputTokens = &out
	v.CostUsd = &cost
	return v
}

// clientJobSummary builds a JobSummary from a job.
func clientJobSummary(j store.Job) JobSummary {
	cs := jobs.ClientStatusFor(j)
	return JobSummary{
		Id:             j.ID,
		Title:          j.Title,
		ClientStatus:   jobSummaryClientStatus(cs),
		StatusText:     jobs.StatusText(cs),
		CurrentVersion: int(j.CurrentVersion),
		CreatedAt:      j.CreatedAt,
		UpdatedAt:      j.UpdatedAt,
	}
}

// clientJobDetail builds the full client view of a job.
func (s *Server) clientJobDetail(ctx context.Context, j store.Job) (ClientJobDetail, error) {
	sum := clientJobSummary(j)
	detail := ClientJobDetail{
		Id:             sum.Id,
		Title:          sum.Title,
		ClientStatus:   clientStatusType(jobs.ClientStatusFor(j)),
		StatusText:     sum.StatusText,
		CurrentVersion: sum.CurrentVersion,
		CreatedAt:      sum.CreatedAt,
		UpdatedAt:      sum.UpdatedAt,
		Prompt:         j.Prompt,
		Question:       j.Question,
		CanCancel:      jobs.CanCancel(j),
		CanRevise:      jobs.CanRevise(j),
		CanAnswer:      jobs.CanAnswer(j),
		StatusSteps:    s.statusSteps(ctx, j),
		InputFiles:     []InputFile{},
		Versions:       []Version{},
		Revisions:      []Revision{},
	}

	input, err := s.store.InputFiles(ctx, j.ID)
	if err != nil {
		return ClientJobDetail{}, err
	}
	for _, f := range input {
		detail.InputFiles = append(detail.InputFiles, InputFile{Path: f.Path, Size: int(f.Size)})
	}

	versions, err := s.versionViews(ctx, j, false)
	if err != nil {
		return ClientJobDetail{}, err
	}
	detail.Versions = versions

	revs, err := s.revisionViews(ctx, j.ID)
	if err != nil {
		return ClientJobDetail{}, err
	}
	detail.Revisions = revs

	// planned documents come from the draft snapshot until v1 exists.
	if j.CurrentVersion == 0 {
		docs, dErr := s.store.DocumentsBySnapshot(ctx, j.ID, store.SnapshotDraft, 0)
		if dErr == nil {
			for _, d := range docs {
				detail.PlannedDocuments = append(detail.PlannedDocuments, PlannedDocument{Title: d.Title, Kind: d.Kind})
			}
		}
	}
	if detail.PlannedDocuments == nil {
		detail.PlannedDocuments = []PlannedDocument{}
	}
	return detail, nil
}

// statusSteps builds the client status window with worker-driven progress.
func (s *Server) statusSteps(ctx context.Context, j store.Job) []StatusStep {
	steps := jobs.StatusSteps(j, s.jobProgress(ctx, j))
	out := make([]StatusStep, 0, len(steps))
	for _, st := range steps {
		out = append(out, StatusStep{Title: st.Title, State: stepStateType(st.State)})
	}
	return out
}

// jobProgress collects the facts StatusSteps needs: when the draft stage
// started and whether codex already produced a trace step for it.
func (s *Server) jobProgress(ctx context.Context, j store.Job) jobs.Progress {
	p := jobs.Progress{Now: s.now()}
	if events, err := s.store.EventsByJob(ctx, j.ID); err == nil {
		for _, e := range events {
			if e.Kind != "stage_started" {
				continue
			}
			var d struct {
				Stage string `json:"stage"`
			}
			if json.Unmarshal([]byte(e.Data), &d) == nil && d.Stage == store.StageDraft {
				t := e.Ts
				p.DraftStartedAt = &t
				break
			}
		}
	}
	if runs, err := s.store.AgentRunsByJob(ctx, j.ID); err == nil {
		for _, r := range runs {
			if r.Agent != "codex" || r.Stage != store.StageDraft {
				continue
			}
			if steps, err := s.store.TraceStepsByRun(ctx, r.ID, 0, 1); err == nil && len(steps) > 0 {
				p.CodexDraftTraced = true
				break
			}
		}
	}
	return p
}

// versionViews builds version views (ascending) with documents.
func (s *Server) versionViews(ctx context.Context, j store.Job, admin bool) ([]Version, error) {
	out := []Version{}
	for v := int64(1); v <= j.CurrentVersion; v++ {
		docs, err := s.store.DocumentsBySnapshot(ctx, j.ID, store.SnapshotVersion, v)
		if err != nil {
			return nil, err
		}
		documents := s.documentViews(ctx, j.ID, docs, admin)
		out = append(out, Version{
			Version:   int(v),
			ReadyAt:   s.versionReadyAt(ctx, j, v),
			Documents: documents,
		})
	}
	return out, nil
}

// versionReadyAt returns when a version became available: the revision
// completion time for v>1, otherwise the job finish time (or updated_at).
func (s *Server) versionReadyAt(ctx context.Context, j store.Job, v int64) time.Time {
	if v > 1 {
		revs, err := s.store.RevisionsByJob(ctx, j.ID)
		if err == nil {
			for _, r := range revs {
				if r.Version == v && r.CompletedAt != nil {
					return *r.CompletedAt
				}
			}
		}
	}
	if j.FinishedAt != nil {
		return *j.FinishedAt
	}
	return j.UpdatedAt
}

func (s *Server) documentViews(ctx context.Context, jobID string, docs []store.Document, admin bool) []Document {
	out := make([]Document, 0, len(docs))
	for _, d := range docs {
		documents := Document{
			Id:        d.ID,
			Idx:       int(d.Idx),
			Title:     d.Title,
			Kind:      d.Kind,
			FilePath:  d.FilePath,
			PageCount: int(d.PageCount),
		}
		if f, err := s.store.FileByBlobKey(ctx, s.versionFileKey(jobID, d)); err == nil {
			documents.Size = int(f.Size)
		}
		switch {
		case admin && d.Snapshot == store.SnapshotDraft:
			documents.DownloadUrl = adminDraftFileURL(jobID, d.FilePath)
		case admin:
			documents.DownloadUrl = adminFileURL(jobID, d.Version, d.FilePath)
		default:
			documents.DownloadUrl = clientFileURL(jobID, d.Version, d.FilePath)
		}
		out = append(out, documents)
	}
	return out
}

// versionFileKey returns the blob key of a document's out/ file for a version.
func (s *Server) versionFileKey(jobID string, d store.Document) string {
	if d.Snapshot == store.SnapshotDraft {
		return fmt.Sprintf("jobs/%s/draft/out/%s", jobID, d.FilePath)
	}
	return fmt.Sprintf("jobs/%s/v%d/out/%s", jobID, d.Version, d.FilePath)
}

func (s *Server) revisionViews(ctx context.Context, jobID string) ([]Revision, error) {
	revs, err := s.store.RevisionsByJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	out := make([]Revision, 0, len(revs))
	for _, r := range revs {
		remarks, rErr := s.store.RemarksByRevision(ctx, r.ID)
		if rErr != nil {
			return nil, rErr
		}
		view := Revision{
			Version:   int(r.Version),
			Comment:   r.Comment,
			CreatedAt: r.CreatedAt,
			Remarks:   remarkViews(remarks),
		}
		if r.CompletedAt != nil {
			view.CompletedAt = *r.CompletedAt
		}
		out = append(out, view)
	}
	return out, nil
}

func remarkViews(remarks []store.Remark) []Remark {
	out := make([]Remark, 0, len(remarks))
	for _, r := range remarks {
		out = append(out, Remark{
			Idx: int(r.Idx), DocumentId: r.DocumentID, Page: int(r.Page),
			X: float32(r.X), Y: float32(r.Y), W: float32(r.W), H: float32(r.H), Text: r.Text,
		})
	}
	return out
}

// ---------- URL builders ----------

func clientFileURL(jobID string, version int64, path string) string {
	return fmt.Sprintf("/api/client/jobs/%s/versions/%d/files/%s", jobID, version, escFileSegment(path))
}

func adminFileURL(jobID string, version int64, path string) string {
	return fmt.Sprintf("/api/admin/jobs/%s/versions/%d/files/%s", jobID, version, escFileSegment(path))
}

// escFileSegment percent-encodes an entire relative path (including '/') so it
// fits a single path segment, which Go's ServeMux requires for the file route.
func escFileSegment(p string) string {
	return url.PathEscape(p)
}

// adminDraftFileURL builds the download URL of a draft document file.
func adminDraftFileURL(jobID, path string) string {
	return fmt.Sprintf("/api/admin/jobs/%s/draft/files/%s", jobID, escFileSegment(path))
}

// pageViewsBase builds Page objects under the given collection base URL
// (e.g. /api/client/jobs/<id>/versions/1 or /api/admin/jobs/<id>/draft).
func pageViewsBase(base, documentID string, pages []store.Page) []Page {
	out := make([]Page, 0, len(pages))
	for _, p := range pages {
		boxes := []ChangedBox{}
		if p.ChangedBoxes != "" {
			_ = json.Unmarshal([]byte(p.ChangedBoxes), &boxes)
		}
		out = append(out, Page{
			Page:         int(p.Page),
			Width:        int(p.Width),
			Height:       int(p.Height),
			ChangedBoxes: boxes,
			ImageUrl:     fmt.Sprintf("%s/pages/%s/%d.png", base, documentID, p.Page),
			ThumbUrl:     fmt.Sprintf("%s/thumbs/%s/%d.png", base, documentID, p.Page),
		})
	}
	return out
}

// latestVerification loads the verification JSON of the newest version that
// has one (found by scanning the job's blobs), so the admin sees it even
// before finish releases the version.
func (s *Server) latestVerification(ctx context.Context, j store.Job) *Verification {
	infos, err := s.blobs.List(ctx, "jobs/"+j.ID)
	if err != nil {
		return nil
	}
	best := -1
	for _, info := range infos {
		var n int
		if _, err := fmt.Sscanf(info.Key, "jobs/"+j.ID+"/v%d/verification.json", &n); err == nil && n > best {
			best = n
		}
	}
	for v := best; v >= 1; v-- {
		rc, _, err := s.blobs.Open(ctx, fmt.Sprintf("jobs/%s/v%d/verification.json", j.ID, v))
		if err != nil {
			continue
		}
		var ver Verification
		dErr := json.NewDecoder(rc).Decode(&ver)
		_ = rc.Close()
		if dErr != nil {
			continue
		}
		if ver.Remaining == nil {
			ver.Remaining = []VerificationItem{}
		}
		return &ver
	}
	return nil
}

// parsePageKey parses "{document_id}/{page}.png" path parameters.
