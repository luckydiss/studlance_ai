package httpapi_test

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/luckydiss/studlance_ai/internal/store"
)

// 1. Полный путь: submit → claim(start, draft) → runs+steps → draft → verify
// → v1 → finish ok → клиент видит done, версию 1, файлы и страницы.
func TestWorkerFullPath(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)

	// register
	code, b := h.wreq(http.MethodPost, "/api/worker/register", wtoken, jsonReader(map[string]interface{}{
		"capabilities": []string{"codex", "claude"}, "info": map[string]interface{}{"os": "windows"},
	}))
	if code != http.StatusOK {
		t.Fatalf("register: %d %s", code, b)
	}
	if decodeAs[map[string]string](t, b)["worker_id"] == "" {
		t.Fatalf("register: no worker_id")
	}

	jobID := h.submitJob(cookie, "Курсовая работа по деталям машин, вариант 14")

	asn := h.claim(wtoken)
	if asn.Action != "start" || asn.Stage != "draft" || asn.Attempt != 0 || asn.Version != 1 {
		t.Fatalf("claim: %+v", asn)
	}
	if asn.Prompt == "" || asn.LeaseExpiresAt == "" {
		t.Fatalf("assignment: %+v", asn)
	}
	// Список входных файлов и их отдача с Range.
	code, b = h.wreq(http.MethodGet, fmt.Sprintf("/api/worker/jobs/%s/input?epoch=%d", jobID, asn.Epoch), wtoken, nil)
	if code != http.StatusOK || !strings.Contains(string(b), "задание.txt") {
		t.Fatalf("input list: %d %s", code, b)
	}
	code, b = h.wreq(http.MethodGet, fmt.Sprintf("/api/worker/jobs/%s/input/задание.txt?epoch=%d", jobID, asn.Epoch), wtoken, nil)
	if code != http.StatusOK || string(b) != "TASK CONTENT" {
		t.Fatalf("input file: %d %q", code, b)
	}
	rangeReq, _ := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/api/worker/jobs/%s/input/задание.txt?epoch=%d", h.srv.URL, jobID, asn.Epoch), nil)
	rangeReq.Header.Set("Authorization", "Bearer "+wtoken)
	rangeReq.Header.Set("Range", "bytes=0-3")
	resp, err := h.hc.Do(rangeReq)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	rbody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(rbody) != "TASK" {
		t.Fatalf("range: %d %q", resp.StatusCode, rbody)
	}

	h.createRunWithSteps(wtoken, jobID, asn, "codex")

	// Первый шаг трейса codex/draft → «Делаем работу» — текущий шаг.
	j := h.clientJob(cookie, jobID)
	if j.ClientStatus != "in_progress" {
		t.Fatalf("running draft: %q", j.ClientStatus)
	}
	for _, s := range j.StatusSteps {
		if s.Title == "Делаем работу" && s.State != "active" {
			t.Fatalf("«Делаем работу» после первого шага codex: %+v", j.StatusSteps)
		}
		if s.Title == "Оформляем по требованиям методички" && s.State != "pending" {
			t.Fatalf("«Оформляем» во время draft: %+v", j.StatusSteps)
		}
	}

	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "draft", "DRAFT DOC")
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "draft", "Готовая записка")

	// stage = verify → «Оформляем» — текущий; клиенту версия ещё не видна.
	j = h.clientJob(cookie, jobID)
	if j.ClientStatus != "in_progress" || len(j.Versions) != 0 {
		t.Fatalf("после draft commit: %+v", j)
	}
	for _, s := range j.StatusSteps {
		if s.Title == "Оформляем по требованиям методички" && s.State != "active" {
			t.Fatalf("«Оформляем» на verify: %+v", j.StatusSteps)
		}
	}

	h.createRunWithSteps(wtoken, jobID, asn, "claude")
	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "v1", "FINAL DOC")
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "v1", "")
	v1 := 1
	h.finish(wtoken, jobID, asn.Epoch, "ok", &v1, nil)

	j = h.clientJob(cookie, jobID)
	if j.ClientStatus != "done" || j.CurrentVersion != 1 {
		t.Fatalf("client: %+v", j)
	}
	if len(j.Versions) != 1 || len(j.Versions[0].Documents) != 1 {
		t.Fatalf("versions: %+v", j.Versions)
	}
	doc := j.Versions[0].Documents[0]
	if doc.FilePath != "записка.docx" || doc.PageCount != 1 {
		t.Fatalf("document: %+v", doc)
	}

	// Файлы и страницы версии доступны клиенту.
	code, b = h.creq(http.MethodGet, doc.DownloadURL, cookie, nil)
	if code != http.StatusOK || string(b) != "FINAL DOC" {
		t.Fatalf("version file: %d %s", code, b)
	}
	code, b = h.creq(http.MethodGet, fmt.Sprintf("/api/client/jobs/%s/versions/1/pages/%s/1.png", jobID, doc.ID), cookie, nil)
	if code != http.StatusOK || string(b) != "PNG-PAGE" {
		t.Fatalf("version page: %d %s", code, b)
	}
	code, _ = h.creq(http.MethodGet, fmt.Sprintf("/api/client/jobs/%s/versions/1/bundle.zip", jobID), cookie, nil)
	if code != http.StatusOK {
		t.Fatalf("bundle: %d", code)
	}

	// «Делаем работу» отмечено готовым (первый шаг трейса codex/draft).
	seen := map[string]string{}
	for _, s := range j.StatusSteps {
		seen[s.Title] = s.State
	}
	if seen["Делаем работу"] != "done" || seen["Готово — версия 1"] != "done" {
		t.Fatalf("steps: %+v", j.StatusSteps)
	}

	// Заголовок взят из commit draft.
	ajob, err := h.st.JobByID(t.Context(), jobID)
	if err != nil || ajob.Title != "Готовая записка" {
		t.Fatalf("title: %q %v", ajob.Title, err)
	}

	// Admin видит verification и agent runs.
	aj := h.adminJob(h.seedUserHTTP("a@local", "pw", store.RoleAdmin), jobID)
	if aj.Verification == nil || aj.Verification.Found != 2 {
		t.Fatalf("verification: %+v", aj.Verification)
	}
	if len(aj.AgentRuns) != 2 {
		t.Fatalf("runs: %+v", aj.AgentRuns)
	}
	_ = asn
}

// 2. Вопрос → needs_input → ответ клиента → claim action: answer.
func TestWorkerQuestionAnswer(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Лабораторная по схемотехнике, нужны расчёты")
	asn := h.claim(wtoken)

	code, b := h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/question", wtoken, jsonReader(map[string]interface{}{
		"epoch": asn.Epoch, "text": "Какой вариант методички?",
	}))
	if code != http.StatusNoContent {
		t.Fatalf("question: %d %s", code, b)
	}
	j := h.clientJob(cookie, jobID)
	if j.ClientStatus != "needs_input" || j.Question == nil || *j.Question != "Какой вариант методички?" {
		t.Fatalf("needs_input: %+v", j)
	}
	// lease снят: heartbeat теперь конфликтует
	code, _ = h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/heartbeat", wtoken, jsonReader(map[string]interface{}{"epoch": asn.Epoch}))
	if code != http.StatusConflict {
		t.Fatalf("heartbeat after question: %d want 409", code)
	}

	code, b = h.creq(http.MethodPost, "/api/client/jobs/"+jobID+"/answer", cookie, jsonReader(map[string]string{"text": "Вариант 14"}))
	if code != http.StatusOK {
		t.Fatalf("answer: %d %s", code, b)
	}

	asn2 := h.claim(wtoken)
	if asn2.Action != "answer" || asn2.Answer == nil || *asn2.Answer != "Вариант 14" {
		t.Fatalf("assignment: %+v", asn2)
	}
	if _, ok := asn2.State["pending_answer"]; ok {
		t.Fatalf("pending_answer не очищен: %+v", asn2.State)
	}
	if asn2.Stage != "draft" || asn2.Attempt != 0 {
		t.Fatalf("stage/attempt: %+v", asn2)
	}
	// Ответ уже выдан и не повторяется. Свободный воркер, зовущий claim снова,
	// получает свой running-заказ как continue (см. п. 6 ревью).
	asn3 := h.claim(wtoken)
	if asn3.Action != "continue" || asn3.Answer != nil {
		t.Fatalf("повторная выдача: %+v", asn3)
	}
	if _, ok := asn3.State["pending_answer"]; ok {
		t.Fatalf("pending_answer выдан повторно: %+v", asn3.State)
	}
}

// 3. Доработка: done → revisions → claim action: revise → вырезка → v2.
func TestWorkerRevision(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Реферат по истории, 15 страниц текста")
	h.workToDone(wtoken, jobID)

	j := h.clientJob(cookie, jobID)
	docID := j.Versions[0].Documents[0].ID

	// Клиент просит доработку с замечанием и файлом.
	body, ct := multipartBody(t, map[string]interface{}{
		"comment": "Поправьте выводы",
		"remarks": []map[string]interface{}{{
			"document_id": docID, "page": 1,
			"x": 0.1, "y": 0.2, "w": 0.5, "h": 0.1,
			"text": "Добавьте ссылки",
		}},
	}, map[string]string{"refs/список.txt": "SOURCES"})
	code, b := h.creqCT(http.MethodPost, "/api/client/jobs/"+jobID+"/revisions", cookie, ct, body)
	if code != http.StatusCreated {
		t.Fatalf("revision: %d %s", code, b)
	}

	asn := h.claim(wtoken)
	if asn.Action != "revise" || asn.Stage != "revise" || asn.Version != 2 {
		t.Fatalf("assignment: %+v", asn)
	}
	if asn.Revision == nil || asn.Revision.Comment != "Поправьте выводы" {
		t.Fatalf("revision: %+v", asn.Revision)
	}
	if len(asn.Revision.Remarks) != 1 || asn.Revision.Remarks[0].Text != "Добавьте ссылки" ||
		asn.Revision.Remarks[0].DocumentTitle != "Пояснительная записка" ||
		asn.Revision.Remarks[0].FilePath != "записка.docx" || asn.Revision.Remarks[0].Page != 1 {
		t.Fatalf("remarks: %+v", asn.Revision.Remarks)
	}
	if len(asn.Revision.Files) != 1 || asn.Revision.Files[0].Path != "revision-2/refs/список.txt" {
		t.Fatalf("revision files: %+v", asn.Revision.Files)
	}

	// Файл доработки скачивается через input.
	code, b = h.wreq(http.MethodGet, fmt.Sprintf("/api/worker/jobs/%s/input/%s?epoch=%d", jobID, url.PathEscape("revision-2/refs/список.txt"), asn.Epoch), wtoken, nil)
	if code != http.StatusOK || string(b) != "SOURCES" {
		t.Fatalf("revision file: %d %s", code, b)
	}

	// Вырезка замечания.
	code, b = h.wreq(http.MethodPut, fmt.Sprintf("/api/worker/jobs/%s/input/revision/2/remarks/1?epoch=%d", jobID, asn.Epoch), wtoken, strings.NewReader("CROP"))
	if code != http.StatusNoContent {
		t.Fatalf("crop: %d %s", code, b)
	}
	if rc, _, err := h.bl.Open(t.Context(), "jobs/"+jobID+"/input/revision-2/remarks/1.png"); err != nil {
		t.Fatalf("crop blob: %v", err)
	} else {
		_ = rc.Close()
	}

	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "v2", "FINAL DOC V2")
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "v2", "")
	v2 := 2
	h.finish(wtoken, jobID, asn.Epoch, "ok", &v2, nil)

	j = h.clientJob(cookie, jobID)
	if j.ClientStatus != "done" || j.CurrentVersion != 2 || len(j.Versions) != 2 {
		t.Fatalf("client: %+v", j)
	}
}
