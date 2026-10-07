package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/luckydiss/studlance_ai/internal/store"
)

// File-only orders (07-web-client.md): a draft can be created with an empty
// prompt, but submit still requires a file or a prompt of 20+ characters.
func TestFileOnlyJobLifecycle(t *testing.T) {
	h := newHarness(t)
	h.seedUser("c@local", "pw", store.RoleClient)
	cookie := h.login("c@local", "pw")

	// Empty prompt creates a draft in uploading.
	rec := h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": ""}), cookie, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create empty prompt: status %d body %s", rec.Code, rec.Body.String())
	}
	job := decode[struct {
		Id           string `json:"id"`
		Title        string `json:"title"`
		Prompt       string `json:"prompt"`
		ClientStatus string `json:"client_status"`
	}](t, rec)
	if job.ClientStatus != "uploading" {
		t.Fatalf("client_status %q want uploading", job.ClientStatus)
	}
	if job.Prompt != "" {
		t.Fatalf("prompt %q want empty", job.Prompt)
	}
	if job.Title != "Заказ" {
		t.Fatalf("title %q want default", job.Title)
	}

	// Submit without files and without a prompt is forbidden.
	rec = h.do(http.MethodPost, "/api/client/jobs/"+job.Id+"/submit", nil, cookie, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("empty submit: status %d want 409 body %s", rec.Code, rec.Body.String())
	}

	// Upload a file, then submit succeeds.
	rec = h.do(http.MethodPut, "/api/client/jobs/"+job.Id+"/input?path="+urlq("задание.pdf"), strings.NewReader("FILEDATA"), cookie, map[string]string{"Content-Type": "application/octet-stream"})
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: status %d", rec.Code)
	}
	rec = h.do(http.MethodPost, "/api/client/jobs/"+job.Id+"/submit", nil, cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit after upload: status %d body %s", rec.Code, rec.Body.String())
	}
	detail := decode[struct {
		ClientStatus string `json:"client_status"`
	}](t, rec)
	if detail.ClientStatus != "accepted" {
		t.Fatalf("client_status after submit %q want accepted", detail.ClientStatus)
	}

	// A short prompt without files does not pass submit either.
	rec = h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": "коротко"}), cookie, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create short prompt: status %d", rec.Code)
	}
	short := decode[struct {
		Id string `json:"id"`
	}](t, rec)
	rec = h.do(http.MethodPost, "/api/client/jobs/"+short.Id+"/submit", nil, cookie, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("short submit: status %d want 409", rec.Code)
	}

	// A text-only order (20+ chars, no files) still works.
	rec = h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": "Запрос на двадцать символов точно есть"}), cookie, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create text-only: status %d", rec.Code)
	}
	text := decode[struct {
		Id string `json:"id"`
	}](t, rec)
	rec = h.do(http.MethodPost, "/api/client/jobs/"+text.Id+"/submit", nil, cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("text-only submit: status %d body %s", rec.Code, rec.Body.String())
	}

	// Whitespace-only prompt is treated as empty.
	rec = h.do(http.MethodPost, "/api/client/jobs", jsonBody(map[string]string{"prompt": "   "}), cookie, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create whitespace prompt: status %d", rec.Code)
	}
	ws := decode[struct {
		Id     string `json:"id"`
		Prompt string `json:"prompt"`
	}](t, rec)
	if ws.Prompt != "" {
		t.Fatalf("whitespace prompt %q want empty", ws.Prompt)
	}
	rec = h.do(http.MethodPost, "/api/client/jobs/"+ws.Id+"/submit", nil, cookie, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("whitespace submit: status %d want 409", rec.Code)
	}
}
