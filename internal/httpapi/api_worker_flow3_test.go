package httpapi_test

// Сценарии 12–14 из задания на PR 3.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/luckydiss/studlance_ai/internal/store"
)

// 12. commit с незагруженной страницей → 400; v<n> с неверным n → 409.
func TestWorkerCommitValidation(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Записка по автоматизации производства")
	asn := h.claim(wtoken)

	// Файл загружен, страница — нет.
	code, b := h.wreq(http.MethodPut, fmt.Sprintf("/api/worker/jobs/%s/snapshot/draft/files?epoch=%d&path=записка.docx", jobID, asn.Epoch), wtoken, strings.NewReader("DOC"))
	if code != http.StatusNoContent {
		t.Fatalf("upload: %d %s", code, b)
	}
	commit := map[string]interface{}{
		"epoch": asn.Epoch,
		"documents": []map[string]interface{}{{
			"idx": 0, "title": "Записка", "kind": "Word", "file_path": "записка.docx",
			"page_count": 1,
			"pages":      []map[string]interface{}{{"page": 1, "width": 10, "height": 10}},
		}},
	}
	code, b = h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/snapshot/draft/commit", wtoken, jsonReader(commit))
	if code != http.StatusBadRequest {
		t.Fatalf("commit без страницы: %d %s", code, b)
	}

	// Снимок v1 на этапе draft — 409.
	code, b = h.wreq(http.MethodPut, fmt.Sprintf("/api/worker/jobs/%s/snapshot/v1/files?epoch=%d&path=записка.docx", jobID, asn.Epoch), wtoken, strings.NewReader("DOC"))
	if code != http.StatusConflict {
		t.Fatalf("v1 на этапе draft: %d %s", code, b)
	}

	// Корректный draft commit.
	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "draft", "DOC")
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "draft", "Записка")

	// На этапе verify ждут v1, не v2.
	code, b = h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/snapshot/v2/commit", wtoken, jsonReader(map[string]interface{}{
		"epoch": asn.Epoch,
		"documents": []map[string]interface{}{{
			"idx": 0, "title": "Записка", "kind": "Word", "file_path": "записка.docx", "page_count": 0, "pages": []interface{}{},
		}},
	}))
	if code != http.StatusConflict {
		t.Fatalf("commit v2 на этапе verify: %d %s", code, b)
	}

	// finish ok без зафиксированного снимка v1 → 409.
	code, b = h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/finish", wtoken, jsonReader(map[string]interface{}{
		"epoch": asn.Epoch, "outcome": "ok", "version": 1,
	}))
	if code != http.StatusConflict {
		t.Fatalf("finish ok без commit: %d %s", code, b)
	}
}

// 13. Неверный токен → 401; клиентская cookie на /api/worker → 401.
func TestWorkerAuth(t *testing.T) {
	h := newWorkerHarness(t)
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)

	code, _ := h.wreq(http.MethodPost, "/api/worker/register", "неверный-токен", jsonReader(map[string]interface{}{
		"capabilities": []string{}, "info": map[string]interface{}{},
	}))
	if code != http.StatusUnauthorized {
		t.Fatalf("неверный токен: %d", code)
	}

	// Без заголовка Authorization, но с клиентской cookie.
	code, _ = h.creq(http.MethodPost, "/api/worker/claim", cookie, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("cookie на /api/worker: %d", code)
	}
	code, _, _ = h.req(http.MethodGet, "/api/worker/jobs/x/input?epoch=1", map[string]string{"Cookie": "sl_session=" + cookie}, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("cookie на /api/worker/jobs: %d", code)
	}
}

// 14. Админ читает черновик и незавершённую версию; клиент получает 404 на
// незавершённую.
func TestAdminDraftAndUnfinishedVersion(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	admin := h.seedUserHTTP("a@local", "pw", store.RoleAdmin)
	jobID := h.submitJob(cookie, "Курсовая по деталям машин, привод цепной")
	asn := h.claim(wtoken)
	h.createRunWithSteps(wtoken, jobID, asn, "codex")
	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "draft", "DRAFT DOC")
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "draft", "Привод цепной")

	aj := h.adminJob(admin, jobID)
	if aj.Draft == nil || len(aj.Draft.Documents) != 1 {
		t.Fatalf("draft: %+v", aj.Draft)
	}
	draftDoc := aj.Draft.Documents[0]
	if !strings.Contains(draftDoc.DownloadURL, "/api/admin/jobs/"+jobID+"/draft/files/") {
		t.Fatalf("draft download url: %s", draftDoc.DownloadURL)
	}

	// Страницы и файлы черновика.
	code, b := h.creq(http.MethodGet, fmt.Sprintf("/api/admin/jobs/%s/draft/documents/%s/pages", jobID, draftDoc.ID), admin, nil)
	if code != http.StatusOK {
		t.Fatalf("draft pages: %d %s", code, b)
	}
	pages := decodeAs[struct {
		Pages []struct {
			Page     int    `json:"page"`
			ImageURL string `json:"image_url"`
		} `json:"pages"`
	}](t, b)
	if len(pages.Pages) != 1 || !strings.Contains(pages.Pages[0].ImageURL, "/draft/pages/") {
		t.Fatalf("draft pages: %+v", pages.Pages)
	}
	code, b = h.creq(http.MethodGet, fmt.Sprintf("/api/admin/jobs/%s/draft/pages/%s/1.png", jobID, draftDoc.ID), admin, nil)
	if code != http.StatusOK || string(b) != "PNG-PAGE" {
		t.Fatalf("draft page: %d", code)
	}
	code, b = h.creq(http.MethodGet, fmt.Sprintf("/api/admin/jobs/%s/draft/thumbs/%s/1.png", jobID, draftDoc.ID), admin, nil)
	if code != http.StatusOK || string(b) != "PNG-THUMB" {
		t.Fatalf("draft thumb: %d", code)
	}
	code, b = h.creq(http.MethodGet, draftDoc.DownloadURL, admin, nil)
	if code != http.StatusOK || string(b) != "DRAFT DOC" {
		t.Fatalf("draft file: %d", code)
	}

	// Незавершённая версия (commit v1 без finish).
	h.createRunWithSteps(wtoken, jobID, asn, "claude")
	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "v1", "FINAL DOC")
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "v1", "")

	aj = h.adminJob(admin, jobID)
	if aj.Verification == nil || aj.Verification.Found != 2 {
		t.Fatalf("verification: %+v", aj.Verification)
	}
	v1Doc := h.adminJobDetailDocID(t, admin, jobID)

	code, b = h.creq(http.MethodGet, fmt.Sprintf("/api/admin/jobs/%s/versions/1/documents/%s/pages", jobID, v1Doc), admin, nil)
	if code != http.StatusOK {
		t.Fatalf("admin v1 pages: %d %s", code, b)
	}
	code, b = h.creq(http.MethodGet, fmt.Sprintf("/api/admin/jobs/%s/versions/1/pages/%s/1.png", jobID, v1Doc), admin, nil)
	if code != http.StatusOK || string(b) != "PNG-PAGE" {
		t.Fatalf("admin v1 page: %d", code)
	}
	code, b = h.creq(http.MethodGet, fmt.Sprintf("/api/admin/jobs/%s/versions/1/files/записка.docx", jobID), admin, nil)
	if code != http.StatusOK || string(b) != "FINAL DOC" {
		t.Fatalf("admin v1 file: %d", code)
	}
	code, _ = h.creq(http.MethodGet, fmt.Sprintf("/api/admin/jobs/%s/versions/1/bundle.zip", jobID), admin, nil)
	if code != http.StatusOK {
		t.Fatalf("admin v1 bundle: %d", code)
	}

	// Клиент на незавершённую версию — 404.
	for _, p := range []string{
		fmt.Sprintf("/api/client/jobs/%s/versions/1/documents/%s/pages", jobID, v1Doc),
		fmt.Sprintf("/api/client/jobs/%s/versions/1/pages/%s/1.png", jobID, v1Doc),
		fmt.Sprintf("/api/client/jobs/%s/versions/1/files/записка.docx", jobID),
		fmt.Sprintf("/api/client/jobs/%s/versions/1/bundle.zip", jobID),
	} {
		code, _ = h.creq(http.MethodGet, p, cookie, nil)
		if code != http.StatusNotFound {
			t.Fatalf("%s: %d want 404", p, code)
		}
	}
}

// adminJobDetailDocID возвращает id документа первой версии из AdminJobDetail.
func (h *wkHarness) adminJobDetailDocID(t *testing.T, admin, jobID string) string {
	t.Helper()
	code, b := h.creq(http.MethodGet, "/api/admin/jobs/"+jobID, admin, nil)
	if code != http.StatusOK {
		t.Fatalf("admin job: %d %s", code, b)
	}
	detail := decodeAs[struct {
		Versions []struct {
			Version   int `json:"version"`
			Documents []struct {
				ID string `json:"id"`
			} `json:"documents"`
		} `json:"versions"`
	}](t, b)
	if len(detail.Versions) != 0 {
		t.Fatalf("незавершённая версия не должна попадать в versions: %+v", detail.Versions)
	}
	// unfinished v1 не видна в versions — документ берём через snapshot v1 напрямую
	docs, err := h.st.DocumentsBySnapshot(t.Context(), jobID, store.SnapshotVersion, 1)
	if err != nil || len(docs) != 1 {
		t.Fatalf("v1 documents: %v %+v", err, docs)
	}
	return docs[0].ID
}
