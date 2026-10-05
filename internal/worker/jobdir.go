package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/luckydiss/studlance_ai/internal/httpapi"
	"github.com/luckydiss/studlance_ai/internal/worker/client"
)

// localState is .studlance/state.json — the worker-side mirror of job state.
type localState struct {
	JobID           string `json:"job_id"`
	Stage           string `json:"stage"`
	Version         int    `json:"version"`
	CodexThreadID   string `json:"codex_thread_id,omitempty"`
	ClaudeSessionID string `json:"claude_session_id,omitempty"`
	QuestionSeq     int    `json:"question_seq"`
	// StageStartedAt persists the stage start so the stage timeout survives
	// a worker restart (continue does not restart the clock).
	StageStartedAt time.Time `json:"stage_started_at,omitempty"`
}

// prepare creates the working folder, downloads input/ (skipping files whose
// sha256 already matches) and writes TASK.md on first preparation
// (05-worker.md «Рабочая папка»).
func (j *jobExec) prepare(ctx context.Context) error {
	for _, d := range []string{
		j.dir,
		filepath.Join(j.dir, "input"),
		j.metaDir(), j.logsDir(), j.snapshotsDir(), j.questionsDir(),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	files, err := j.w.cl.ListInput(ctx, j.asn.JobId, j.asn.Epoch)
	if err != nil {
		return err
	}
	j.inputFiles = files
	for _, f := range files {
		if err := j.downloadInput(ctx, f); err != nil {
			return err
		}
	}

	if _, err := os.Stat(filepath.Join(j.dir, "TASK.md")); errors.Is(err, os.ErrNotExist) {
		if err := j.writeTaskMD(); err != nil {
			return err
		}
	}
	j.loadState()
	return nil
}

// downloadInput fetches one input file unless the local copy already matches.
// Server paths that are invalid on Windows get a deterministic local name.
func (j *jobExec) downloadInput(ctx context.Context, f httpapi.WorkerInputFile) error {
	rel, err := sanitizeRel(f.Path)
	if err != nil {
		return fmt.Errorf("input path %q: %w", f.Path, err)
	}
	rel = mapLocalName(rel)
	dst := filepath.Join(j.dir, "input", filepath.FromSlash(rel))
	if sameFileHash(dst, f.Sha256) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// Download with retries and a sha256 check (a corrupt transfer is
	// downloaded again); stale lease and other 4xx are not retried.
	backoff := 2 * time.Second
	for {
		tmp, err := os.CreateTemp(filepath.Dir(dst), ".dl-*")
		if err != nil {
			return err
		}
		err = j.w.cl.GetInput(ctx, j.asn.JobId, j.asn.Epoch, f.Path, tmp)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil && !sameFileHash(tmp.Name(), f.Sha256) {
			err = fmt.Errorf("sha256 mismatch for %s", f.Path)
		}
		if err == nil {
			if err := os.Rename(tmp.Name(), dst); err != nil {
				return fmt.Errorf("place %s: %w", f.Path, err)
			}
			return nil
		}
		_ = os.Remove(tmp.Name())
		if !client.IsRetryable(err) || ctx.Err() != nil {
			return fmt.Errorf("download %s: %w", f.Path, err)
		}
		j.log.Error("download failed, retrying", "path", f.Path, "err", err)
		if !sleepCtx(ctx, backoff) {
			return fmt.Errorf("download %s: %w", f.Path, err)
		}
		if backoff < 60*time.Second {
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
		}
	}
}

// writeTaskMD writes TASK.md: the client prompt plus the list of initial
// (revision 0) input files.
func (j *jobExec) writeTaskMD() error {
	var b strings.Builder
	b.WriteString("# Задание клиента\n\n")
	b.WriteString(j.asn.Prompt)
	b.WriteString("\n\n# Приложенные файлы\n")
	any := false
	for _, f := range j.inputFiles {
		if f.Revision != 0 {
			continue
		}
		any = true
		fmt.Fprintf(&b, "- input/%s (%s)\n", localPath(f.Path), humanSize(f.Size))
	}
	if !any {
		b.WriteString("—\n")
	}
	return os.WriteFile(filepath.Join(j.dir, "TASK.md"), []byte(b.String()), 0o644)
}

// filesLine returns the prompt {{.Files}} listing of initial input files.
func (j *jobExec) filesLine() string {
	var b strings.Builder
	for _, f := range j.inputFiles {
		if f.Revision != 0 {
			continue
		}
		fmt.Fprintf(&b, "- input/%s (%s)\n", localPath(f.Path), humanSize(f.Size))
	}
	return strings.TrimRight(b.String(), "\n")
}

// localPath maps a server-side input path to the name on disk.
func localPath(p string) string {
	clean, err := sanitizeRel(p)
	if err != nil {
		return "_"
	}
	return mapLocalName(clean)
}

// ---------- state.json ----------

func (j *jobExec) loadState() {
	j.state = localState{JobID: j.asn.JobId, Stage: string(j.asn.Stage), Version: j.asn.Version}
	raw, err := os.ReadFile(j.statePath())
	if err != nil {
		return
	}
	var st localState
	if json.Unmarshal(raw, &st) == nil && st.JobID == j.asn.JobId {
		j.state = st
	}
}

func (j *jobExec) saveState() {
	j.state.Stage = j.stage
	j.state.Version = j.asn.Version
	raw, err := json.MarshalIndent(j.state, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(j.statePath(), raw, 0o644)
}

// sessionFor returns the known session id of the agent: server state from the
// assignment wins, the local state.json is the fallback.
func (j *jobExec) sessionFor(agent string) string {
	key := "codex_thread_id"
	if agent == "claude" {
		key = "claude_session_id"
	}
	if j.asn.State != nil {
		if v, ok := (*j.asn.State)[key].(string); ok && v != "" {
			return v
		}
	}
	if agent == "claude" {
		return j.state.ClaudeSessionID
	}
	return j.state.CodexThreadID
}

func (j *jobExec) setSession(agent, session string) {
	if agent == "claude" {
		j.state.ClaudeSessionID = session
	} else {
		j.state.CodexThreadID = session
	}
	j.saveState()
}

// setState merges keys into jobs.state on the server and mirrors session ids
// locally. Stale lease propagates as an error for the caller to abort on.
func (j *jobExec) setState(ctx context.Context, kv map[string]interface{}) error {
	return j.w.cl.SetState(ctx, j.asn.JobId, j.asn.Epoch, kv)
}

// ---------- paths ----------

func (j *jobExec) metaDir() string      { return filepath.Join(j.dir, ".studlance") }
func (j *jobExec) logsDir() string      { return filepath.Join(j.metaDir(), "logs") }
func (j *jobExec) snapshotsDir() string { return filepath.Join(j.metaDir(), "snapshots") }
func (j *jobExec) questionsDir() string { return filepath.Join(j.metaDir(), "questions") }
func (j *jobExec) statePath() string    { return filepath.Join(j.metaDir(), "state.json") }

// sanitizeRel validates a slash-separated relative path.
func sanitizeRel(p string) (string, error) {
	// Note: ':' is NOT rejected here — mapLocalName rewrites it afterwards.
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsRune(p, '\\') {
		return "", errors.New("bad path")
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == "." {
		return "", errors.New("bad path")
	}
	return clean, nil
}

// windowsReserved are device names that cannot be file names on Windows.
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// mapLocalName rewrites a slash-separated relative path into a Windows-safe
// one: invalid characters (: ? * " < > | and control chars) become "_",
// trailing dots and spaces are dropped, reserved device names get a "_"
// suffix. Deterministic; applied on every OS so tests match production.
func mapLocalName(rel string) string {
	segs := strings.Split(rel, "/")
	for i, seg := range segs {
		segs[i] = mapLocalSegment(seg)
	}
	return strings.Join(segs, "/")
}

func mapLocalSegment(seg string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r < 32, r == '<', r == '>', r == ':', r == '"', r == '|', r == '?', r == '*':
			return '_'
		}
		return r
	}, seg)
	mapped = strings.TrimRight(mapped, ". ")
	if mapped == "" {
		mapped = "_"
	}
	// Reserved names compare on the base name (before any extension); the "_"
	// goes between base and extension: con.docx → con_.docx.
	base, ext := mapped, ""
	if i := strings.IndexByte(mapped, '.'); i >= 0 {
		base, ext = mapped[:i], mapped[i:]
	}
	if windowsReserved[strings.ToUpper(base)] {
		mapped = base + "_" + ext
	}
	return mapped
}

func sha256Sum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sameFileHash reports whether path exists and its sha256 equals want (hex).
func sameFileHash(path, want string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == want
}

// humanSize renders bytes like «1.2 МБ».
func humanSize(n int) string {
	const mb = 1024 * 1024
	const kb = 1024
	switch {
	case n >= mb:
		return fmt.Sprintf("%.1f МБ", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1f КБ", float64(n)/kb)
	default:
		return fmt.Sprintf("%d Б", n)
	}
}

// isStale reports whether err ends the job for this worker.
func isStale(err error) bool {
	return errors.Is(err, client.ErrStaleLease)
}
