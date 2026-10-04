package httpapi_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckydiss/studlance_ai/internal/store"
)

// createDraftJob creates a job for the given cookie and submits it.
func createDraftJob(t *testing.T, h *harness, cookie *http.Cookie, prompt string) string {
	t.Helper()
	rec := h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": prompt}), cookie, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create job: %d body %s", rec.Code, rec.Body.String())
	}
	job := decode[map[string]interface{}](t, rec)
	id := job["id"].(string)
	rec = h.do(http.MethodPost, "/api/client/jobs/"+id+"/submit", nil, cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit job: %d body %s", rec.Code, rec.Body.String())
	}
	return id
}

// seedVersion writes a blob at the version out/ path so downloads can be tested.
func seedVersionBlob(t *testing.T, h *harness, jobID string, version int64, path, content string) {
	t.Helper()
	key := "jobs/" + jobID + "/v" + itoa(version) + "/out/" + path
	if _, _, err := h.blobs.Put(testCtx(), key, strings.NewReader(content)); err != nil {
		t.Fatalf("put version blob: %v", err)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// TestReuploadSamePathKeepsFile reproduces the bug where re-uploading the same
// path deleted the freshly written blob (old blob key == new blob key).
func TestReuploadSamePathKeepsFile(t *testing.T) {
	h := newHarness(t)
	h.seedUser("c@local", "pw", store.RoleClient)
	cookie := h.login("c@local", "pw")

	rec := h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": "Заказ для повторной загрузки файла"}), cookie, map[string]string{"Content-Type": "application/json"})
	job := decode[map[string]interface{}](t, rec)
	jobID := job["id"].(string)

	url := "/api/client/jobs/" + jobID + "/input?path=docs%2Ffile.txt"
	rec = h.do(http.MethodPut, url, strings.NewReader("FIRST"), cookie, map[string]string{"Content-Type": "application/octet-stream"})
	if rec.Code != http.StatusOK {
		t.Fatalf("first upload: %d %s", rec.Code, rec.Body.String())
	}
	rec = h.do(http.MethodPut, url, strings.NewReader("SECOND-LONGER"), cookie, map[string]string{"Content-Type": "application/octet-stream"})
	if rec.Code != http.StatusOK {
		t.Fatalf("second upload: %d %s", rec.Code, rec.Body.String())
	}

	// Blob must still exist with the new content.
	rc, info, err := h.blobs.Open(testCtx(), "jobs/"+jobID+"/input/docs/file.txt")
	if err != nil {
		t.Fatalf("blob missing after re-upload: %v", err)
	}
	got := make([]byte, info.Size)
	_, _ = rc.Read(got)
	_ = rc.Close()
	if string(got) != "SECOND-LONGER" {
		t.Fatalf("blob content %q", got)
	}

	// Exactly one file record with the new content/size.
	if _, err := h.store.InputFileByPath(testCtx(), jobID, "docs/file.txt"); err != nil {
		t.Fatalf("file record missing: %v", err)
	}
	files, err := h.store.InputFiles(testCtx(), jobID)
	if err != nil {
		t.Fatalf("input files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 input file, got %d", len(files))
	}
	if files[0].Size != int64(len("SECOND-LONGER")) {
		t.Fatalf("file size %d", files[0].Size)
	}
}

// TestUploadTooLarge returns 413 before writing to disk and does not corrupt
// the existing file.
func TestUploadTooLarge(t *testing.T) {
	h := newHarness(t)
	h.seedUser("c@local", "pw", store.RoleClient)
	cookie := h.login("c@local", "pw")

	rec := h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": "Заказ для проверки лимита размера"}), cookie, map[string]string{"Content-Type": "application/json"})
	job := decode[map[string]interface{}](t, rec)
	jobID := job["id"].(string)

	// h.cfg.MaxUpload is 1 MiB; send 1 MiB + 1 byte.
	big := strings.Repeat("x", (1<<20)+1)
	rec = h.do(http.MethodPut, "/api/client/jobs/"+jobID+"/input?path=big.bin", strings.NewReader(big), cookie, map[string]string{"Content-Type": "application/octet-stream"})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d want 413 body %s", rec.Code, rec.Body.String())
	}
	if rc, _, err := h.blobs.Open(testCtx(), "jobs/"+jobID+"/input/big.bin"); err == nil {
		_ = rc.Close()
		t.Fatal("oversized blob was written to disk")
	}
}

// TestRevisionRemarkValidation verifies document ownership/version, page range,
// coordinates and text length.
func TestRevisionRemarkValidation(t *testing.T) {
	h := newHarness(t)
	owner := h.seedUser("owner@local", "pw", store.RoleClient)
	cookie := h.login("owner@local", "pw")
	jobID := createDraftJob(t, h, cookie, "Заказ готовой версии для проверки замечаний")

	// mark done with version 1 and one document with 2 pages
	docID := newID()
	if err := h.store.ReplaceDocuments(testCtx(), jobID, store.SnapshotVersion, 1, []store.DocumentWithPages{{
		Document: store.Document{ID: docID, JobID: jobID, Snapshot: store.SnapshotVersion, Version: 1, Idx: 0, Title: "Записка", Kind: "Word", FilePath: "z.docx", PageCount: 2},
	}}); err != nil {
		t.Fatalf("replace docs: %v", err)
	}
	j, _ := h.store.JobByID(testCtx(), jobID)
	j.Status = store.StatusDone
	j.CurrentVersion = 1
	now := time.Now().UTC()
	j.FinishedAt = &now
	if err := h.store.UpdateJob(testCtx(), j); err != nil {
		t.Fatalf("update: %v", err)
	}
	_ = owner

	mkRemark := func(m map[string]interface{}) string {
		data := map[string]interface{}{"comment": "", "remarks": []interface{}{m}}
		return mustJSON(data)
	}

	// invalid cases first (job is still done / can revise)
	// unknown document
	bad := map[string]interface{}{"document_id": "nope", "page": 1, "x": 0.1, "y": 0.1, "w": 0.2, "h": 0.2, "text": "x"}
	if rec := h.doRevision(t, cookie, jobID, mkRemark(bad), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown document: %d", rec.Code)
	}
	// page out of range
	bad = map[string]interface{}{"document_id": docID, "page": 3, "x": 0.1, "y": 0.1, "w": 0.2, "h": 0.2, "text": "x"}
	if rec := h.doRevision(t, cookie, jobID, mkRemark(bad), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("page range: %d", rec.Code)
	}
	// x+w > 1
	bad = map[string]interface{}{"document_id": docID, "page": 1, "x": 0.9, "y": 0.1, "w": 0.2, "h": 0.2, "text": "x"}
	if rec := h.doRevision(t, cookie, jobID, mkRemark(bad), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("x+w>1: %d", rec.Code)
	}
	// zero size
	bad = map[string]interface{}{"document_id": docID, "page": 1, "x": 0.1, "y": 0.1, "w": 0, "h": 0.2, "text": "x"}
	if rec := h.doRevision(t, cookie, jobID, mkRemark(bad), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("w=0: %d", rec.Code)
	}
	// empty text
	bad = map[string]interface{}{"document_id": docID, "page": 1, "x": 0.1, "y": 0.1, "w": 0.2, "h": 0.2, "text": ""}
	if rec := h.doRevision(t, cookie, jobID, mkRemark(bad), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty text: %d", rec.Code)
	}
	// text too long
	bad = map[string]interface{}{"document_id": docID, "page": 1, "x": 0.1, "y": 0.1, "w": 0.2, "h": 0.2, "text": strings.Repeat("я", 2001)}
	if rec := h.doRevision(t, cookie, jobID, mkRemark(bad), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("long text: %d", rec.Code)
	}

	// valid remark (moves the job to revise)
	valid := map[string]interface{}{"document_id": docID, "page": 1, "x": 0.1, "y": 0.1, "w": 0.2, "h": 0.2, "text": "Поправить"}
	rec := h.doRevision(t, cookie, jobID, mkRemark(valid), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid remark: %d %s", rec.Code, rec.Body.String())
	}
}

// TestVersionResourcesRequireReleasedVersion verifies v > current_version is 404.
func TestVersionResourcesRequireReleasedVersion(t *testing.T) {
	h := newHarness(t)
	h.seedUser("c@local", "pw", store.RoleClient)
	cookie := h.login("c@local", "pw")
	jobID := createDraftJob(t, h, cookie, "Заказ без выпущенной версии для проверки 404")

	// No version yet: v=1 is not released.
	for _, p := range []string{
		"/api/client/jobs/" + jobID + "/versions/1/files/z.docx",
		"/api/client/jobs/" + jobID + "/versions/1/bundle.zip",
		"/api/client/jobs/" + jobID + "/versions/1/pages/doc/1.png",
	} {
		rec := h.do(http.MethodGet, p, nil, cookie, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d want 404", p, rec.Code)
		}
	}

	// Release v1 (blob + done), v1 works, v2 still 404.
	seedVersionBlob(t, h, jobID, 1, "z.docx", "DOC")
	j, _ := h.store.JobByID(testCtx(), jobID)
	j.Status = store.StatusDone
	j.CurrentVersion = 1
	now := time.Now().UTC()
	j.FinishedAt = &now
	if err := h.store.UpdateJob(testCtx(), j); err != nil {
		t.Fatalf("update: %v", err)
	}
	rec := h.do(http.MethodGet, "/api/client/jobs/"+jobID+"/versions/1/files/z.docx", nil, cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("v1 file: %d %s", rec.Code, rec.Body.String())
	}
	for _, p := range []string{
		"/api/client/jobs/" + jobID + "/versions/2/files/z.docx",
		"/api/client/jobs/" + jobID + "/versions/2/bundle.zip",
	} {
		rec := h.do(http.MethodGet, p, nil, cookie, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d want 404", p, rec.Code)
		}
	}
}

// TestRevisionFilePathFromContentDisposition verifies nested paths survive.
func TestRevisionFilePathFromContentDisposition(t *testing.T) {
	h := newHarness(t)
	h.seedUser("c@local", "pw", store.RoleClient)
	cookie := h.login("c@local", "pw")
	jobID := createDraftJob(t, h, cookie, "Заказ для проверки путей файлов доработки")

	j, _ := h.store.JobByID(testCtx(), jobID)
	j.Status = store.StatusDone
	j.CurrentVersion = 1
	now := time.Now().UTC()
	j.FinishedAt = &now
	if err := h.store.UpdateJob(testCtx(), j); err != nil {
		t.Fatalf("update: %v", err)
	}

	rec := h.doRevision(t, cookie, jobID, `{"comment":"переделать"}`, map[string]string{
		"refs/old note.txt": "CONTENT",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("revision: %d %s", rec.Code, rec.Body.String())
	}
	rc, _, err := h.blobs.Open(testCtx(), "jobs/"+jobID+"/input/revision-2/refs/old note.txt")
	if err != nil {
		t.Fatalf("nested revision file not stored: %v", err)
	}
	_ = rc.Close()
}

// TestBundleZipRangeStreams verifies bundle.zip is served from blobs with Range.
func TestBundleZipRangeStreams(t *testing.T) {
	h := newHarness(t)
	h.seedUser("c@local", "pw", store.RoleClient)
	cookie := h.login("c@local", "pw")
	jobID := createDraftJob(t, h, cookie, "Заказ с архивом для проверки Range-запросов")

	seedVersionBlob(t, h, jobID, 1, "записка.docx", strings.Repeat("A", 5000))
	j, _ := h.store.JobByID(testCtx(), jobID)
	j.Status = store.StatusDone
	j.CurrentVersion = 1
	now := time.Now().UTC()
	j.FinishedAt = &now
	if err := h.store.UpdateJob(testCtx(), j); err != nil {
		t.Fatalf("update: %v", err)
	}

	// full bundle
	rec := h.do(http.MethodGet, "/api/client/jobs/"+jobID+"/versions/1/bundle.zip", nil, cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bundle: %d %s", rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "записка.docx" {
		t.Fatalf("zip entries: %+v", zr.File)
	}

	// ranged request
	req := h.newRequest(http.MethodGet, "/api/client/jobs/"+jobID+"/versions/1/bundle.zip", nil, cookie)
	req.Header.Set("Range", "bytes=0-3")
	rec2 := h.serve(req)
	if rec2.Code != http.StatusPartialContent {
		t.Fatalf("range status %d want 206", rec2.Code)
	}
	if rec2.Body.Len() != 4 {
		t.Fatalf("range length %d want 4", rec2.Body.Len())
	}
}

// TestStatusStepsOrdering verifies done/active/pending and that during verify
// the "Делаем работу" step is done.
func TestStatusStepsOrdering(t *testing.T) {
	h := newHarness(t)
	h.seedUser("c@local", "pw", store.RoleClient)
	cookie := h.login("c@local", "pw")
	jobID := createDraftJob(t, h, cookie, "Заказ для проверки шагов окошка статуса")

	// Running verify, version 0
	j, _ := h.store.JobByID(testCtx(), jobID)
	j.Status = store.StatusRunning
	j.Stage = store.StageVerify
	if err := h.store.UpdateJob(testCtx(), j); err != nil {
		t.Fatalf("update: %v", err)
	}
	steps := fetchSteps(t, h, cookie, jobID)
	want := []string{"done", "done", "done", "active", "pending"}
	if strings.Join(steps, ",") != strings.Join(want, ",") {
		t.Fatalf("verify steps %v want %v", steps, want)
	}

	// Done
	j.Status = store.StatusDone
	j.Stage = ""
	j.CurrentVersion = 1
	now := time.Now().UTC()
	j.FinishedAt = &now
	if err := h.store.UpdateJob(testCtx(), j); err != nil {
		t.Fatalf("update done: %v", err)
	}
	steps = fetchSteps(t, h, cookie, jobID)
	want = []string{"done", "done", "done", "done", "done"}
	if strings.Join(steps, ",") != strings.Join(want, ",") {
		t.Fatalf("done steps %v want %v", steps, want)
	}
}

func fetchSteps(t *testing.T, h *harness, cookie *http.Cookie, jobID string) []string {
	t.Helper()
	rec := h.do(http.MethodGet, "/api/client/jobs/"+jobID, nil, cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get job: %d", rec.Code)
	}
	var detail struct {
		Steps []struct {
			State string `json:"state"`
		} `json:"status_steps"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	out := make([]string, 0, len(detail.Steps))
	for _, s := range detail.Steps {
		out = append(out, s.State)
	}
	return out
}

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// doRevision posts a multipart revision with the given data and files.
func (h *harness) doRevision(t *testing.T, cookie *http.Cookie, jobID, data string, files map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("data", data)
	for name, content := range files {
		fw, _ := w.CreateFormFile("files", name)
		_, _ = fw.Write([]byte(content))
	}
	_ = w.Close()
	return h.do(http.MethodPost, "/api/client/jobs/"+jobID+"/revisions", &buf, cookie, map[string]string{"Content-Type": w.FormDataContentType()})
}
