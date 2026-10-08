package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckydiss/studlance_ai/internal/store"
)

// seedInputFile writes a blob and a files row for an input of the given kind.
func seedInputFile(t *testing.T, h *harness, jobID, path string, version int64, content string) {
	t.Helper()
	key := "jobs/" + jobID + "/input/" + path
	if _, _, err := h.blobs.Put(testCtx(), key, strings.NewReader(content)); err != nil {
		t.Fatalf("put input blob: %v", err)
	}
	if err := h.store.CreateFile(testCtx(), store.File{
		ID: newID(), JobID: jobID, Kind: store.FileInput, Version: version,
		Path: path, BlobKey: key, Size: int64(len(content)), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create file: %v", err)
	}
}

// TestAdminInputDownload covers the admin GET of source files and revision
// attachments: role checks, exact path matching, revision from metadata and
// rejection of another job's file, unknown paths and "..".
func TestAdminInputDownload(t *testing.T) {
	h := newHarness(t)
	h.seedUser("c@local", "pw", store.RoleClient)
	h.seedUser("a@local", "pw", store.RoleAdmin)
	client := h.login("c@local", "pw")
	admin := h.login("a@local", "pw")

	// A job with one real source file and one revision attachment.
	rec := h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": "Заказ с исходником и вложением доработки"}), client, map[string]string{"Content-Type": "application/json"})
	jobID := decode[map[string]interface{}](t, rec)["id"].(string)
	seedInputFile(t, h, jobID, "задание.txt", 0, "SOURCE-BYTES")
	seedInputFile(t, h, jobID, "input/revision-2/замечания.txt", 2, "REVISION-ATTACH")
	seedInputFile(t, h, jobID, "папка с пробелом/файл задания.txt", 0, "FOLDER-SPACE-BYTES")

	// An initial source whose path merely looks like a revision path stays a
	// source (revision comes from files.version, not the folder name).
	seedInputFile(t, h, jobID, "input/revision-2/исходник.txt", 0, "REAL-SOURCE")

	// A second job with the same relative path must not leak.
	rec = h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": "Второй заказ с тем же именем файла"}), client, map[string]string{"Content-Type": "application/json"})
	otherID := decode[map[string]interface{}](t, rec)["id"].(string)
	seedInputFile(t, h, otherID, "задание.txt", 0, "OTHER-JOB-BYTES")
	// Crops live in the file table too, but are never admin input downloads.
	cropKey := "jobs/" + jobID + "/revision-crops/2/1.png"
	if _, _, err := h.blobs.Put(testCtx(), cropKey, strings.NewReader("PRIVATE-CROP")); err != nil {
		t.Fatalf("put crop blob: %v", err)
	}
	if err := h.store.CreateFile(testCtx(), store.File{
		ID: newID(), JobID: jobID, Kind: store.FileCrop, Version: 2,
		Path: "revision-crops/2/1.png", BlobKey: cropKey, Size: int64(len("PRIVATE-CROP")), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create crop: %v", err)
	}

	// Admin detail groups the files with the right revision numbers.
	rec = h.do(http.MethodGet, "/api/admin/jobs/"+jobID, nil, admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin detail: %d %s", rec.Code, rec.Body.String())
	}
	detail := decode[struct {
		InputFiles []struct {
			Path     string `json:"path"`
			Size     int    `json:"size"`
			Revision int    `json:"revision"`
		} `json:"input_files"`
	}](t, rec)
	byPath := map[string]int{}
	for _, f := range detail.InputFiles {
		byPath[f.Path] = f.Revision
	}
	if byPath["задание.txt"] != 0 {
		t.Fatalf("source revision = %d want 0 (%+v)", byPath["задание.txt"], detail.InputFiles)
	}
	if byPath["input/revision-2/замечания.txt"] != 2 {
		t.Fatalf("attachment revision = %d want 2 (%+v)", byPath["input/revision-2/замечания.txt"], detail.InputFiles)
	}
	if byPath["input/revision-2/исходник.txt"] != 0 {
		t.Fatalf("lookalike source revision = %d want 0 (%+v)", byPath["input/revision-2/исходник.txt"], detail.InputFiles)
	}

	// Download the source and the attachment with exact bytes.
	for path, want := range map[string]string{
		"задание.txt":                       "SOURCE-BYTES",
		"input/revision-2/замечания.txt":    "REVISION-ATTACH",
		"input/revision-2/исходник.txt":     "REAL-SOURCE",
		"папка с пробелом/файл задания.txt": "FOLDER-SPACE-BYTES",
	} {
		rec = h.do(http.MethodGet, "/api/admin/jobs/"+jobID+"/input/"+urlq(path), nil, admin, nil)
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Fatalf("download %q: status %d body %q want %q", path, rec.Code, rec.Body.String(), want)
		}
		if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "filename*=UTF-8''") {
			t.Fatalf("download %q content-disposition %q", path, cd)
		}
	}

	// The same relative path in another job returns that job's bytes only.
	rec = h.do(http.MethodGet, "/api/admin/jobs/"+otherID+"/input/"+urlq("задание.txt"), nil, admin, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "OTHER-JOB-BYTES" {
		t.Fatalf("other job download: status %d body %q", rec.Code, rec.Body.String())
	}

	// Anonymous → 401, client → 403, unknown job/file → 404, ".." → 404.
	rec = h.do(http.MethodGet, "/api/admin/jobs/"+jobID+"/input/"+urlq("задание.txt"), nil, nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d want 401", rec.Code)
	}
	rec = h.do(http.MethodGet, "/api/admin/jobs/"+jobID+"/input/"+urlq("задание.txt"), nil, client, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("client: %d want 403", rec.Code)
	}
	rec = h.do(http.MethodGet, "/api/admin/jobs/unknown-job/input/"+urlq("задание.txt"), nil, admin, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown job: %d want 404", rec.Code)
	}
	rec = h.do(http.MethodGet, "/api/admin/jobs/"+jobID+"/input/"+urlq("нет-такого.txt"), nil, admin, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown file: %d want 404", rec.Code)
	}
	rec = h.do(http.MethodGet, "/api/admin/jobs/"+jobID+"/input/"+urlq("../escape.txt"), nil, admin, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("dotdot: %d want 404", rec.Code)
	}
	rec = h.do(http.MethodGet, "/api/admin/jobs/"+jobID+"/input/"+urlq("revision-crops/2/1.png"), nil, admin, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("crop: %d want 404", rec.Code)
	}
	// A path that normalizes to a real file is still not the exact listed path.
	rec = h.do(http.MethodGet, "/api/admin/jobs/"+jobID+"/input/"+urlq("/задание.txt"), nil, admin, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-exact path: %d want 404", rec.Code)
	}
}
