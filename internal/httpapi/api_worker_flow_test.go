package httpapi_test

import (
	"context"
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
	if rc, _, err := h.bl.Open(t.Context(), "jobs/"+jobID+"/revision-crops/2/1.png"); err != nil {
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

// blobString reads a blob written by the API.
func (h *wkHarness) blobString(t *testing.T, key string) string {
	t.Helper()
	rc, _, err := h.bl.Open(t.Context(), key)
	if err != nil {
		t.Fatalf("blob %s: %v", key, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// submitJobWithFiles creates a job, uploads the given input files and submits it.
func (h *wkHarness) submitJobWithFiles(cookie, prompt string, files map[string]string) string {
	h.t.Helper()
	code, b := h.creq(http.MethodPost, "/api/client/jobs", cookie, jsonReader(map[string]string{"prompt": prompt}))
	if code != http.StatusCreated {
		h.t.Fatalf("create job: %d %s", code, b)
	}
	job := decodeAs[tClientJob](h.t, b)
	for path, content := range files {
		code, b = h.creq(http.MethodPut, "/api/client/jobs/"+job.ID+"/input?path="+url.QueryEscape(path), cookie, strings.NewReader(content))
		if code != http.StatusOK {
			h.t.Fatalf("upload %s: %d %s", path, code, b)
		}
	}
	code, b = h.creq(http.MethodPost, "/api/client/jobs/"+job.ID+"/submit", cookie, nil)
	if code != http.StatusOK {
		h.t.Fatalf("submit: %d %s", code, b)
	}
	return job.ID
}

// revisionCropJob drives a job to done, creates a revision with one remark
// (plus an optional attachment) and claims the revise assignment.
func revisionCropJob(t *testing.T, h *wkHarness, wtoken, cookie, jobID, attachment string) tAssignment {
	t.Helper()
	h.workToDone(wtoken, jobID)
	j := h.clientJob(cookie, jobID)
	docID := j.Versions[0].Documents[0].ID
	files := map[string]string{}
	if attachment != "" {
		files["remarks/1.png"] = attachment
	}
	body, ct := multipartBody(t, map[string]interface{}{
		"comment": "Поправьте выводы",
		"remarks": []map[string]interface{}{{
			"document_id": docID, "page": 1,
			"x": 0.1, "y": 0.1, "w": 0.2, "h": 0.2,
			"text": "Добавьте ссылки",
		}},
	}, files)
	code, b := h.creqCT(http.MethodPost, "/api/client/jobs/"+jobID+"/revisions", cookie, ct, body)
	if code != http.StatusCreated {
		t.Fatalf("revision: %d %s", code, b)
	}
	asn := h.claim(wtoken)
	if asn.Action != "revise" || asn.Stage != "revise" {
		t.Fatalf("assignment: %+v", asn)
	}
	return asn
}

// Пункт 2 (третий раунд): вырезка замечания отделена от входных файлов
// клиента. Приложенный remarks/1.png (и начальный вход с тем же путём) не
// перезаписывается вырезкой, запись crop и её blob отдельные, повторный и
// ошибочный upload не трогают входной blob.
func TestWorkerRevisionCropSeparateFromInput(t *testing.T) {
	const attachment = "REVISION ATTACHMENT"

	t.Run("revision attachment remarks/1.png", func(t *testing.T) {
		h := newWorkerHarness(t)
		wtoken := h.seedWorker("pc-1")
		cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
		jobID := h.submitJob(cookie, "Реферат по истории")
		asn := revisionCropJob(t, h, wtoken, cookie, jobID, attachment)
		// The attachment named remarks/1.png is an ordinary client input and
		// must reach the worker as such, not be hidden as a crop namespace.
		foundAttachment := false
		for _, f := range asn.Revision.Files {
			if f.Path == "revision-2/remarks/1.png" {
				foundAttachment = true
			}
		}
		if !foundAttachment {
			t.Fatalf("attachment missing from assignment files: %+v", asn.Revision.Files)
		}

		inputKey := "jobs/" + jobID + "/input/revision-2/remarks/1.png"
		cropKey := "jobs/" + jobID + "/revision-crops/2/1.png"
		if got := h.blobString(t, inputKey); got != attachment {
			t.Fatalf("input blob = %q, want %q", got, attachment)
		}

		// The crop is uploaded twice (idempotent).
		for i := 0; i < 2; i++ {
			code, b := h.wreq(http.MethodPut,
				fmt.Sprintf("/api/worker/jobs/%s/input/revision/2/remarks/1?epoch=%d", jobID, asn.Epoch),
				wtoken, strings.NewReader("CROP"))
			if code != http.StatusNoContent {
				t.Fatalf("crop #%d: %d %s", i+1, code, b)
			}
		}
		if got := h.blobString(t, cropKey); got != "CROP" {
			t.Fatalf("crop blob = %q, want CROP", got)
		}
		if got := h.blobString(t, inputKey); got != attachment {
			t.Fatalf("crop upload overwrote the input blob: %q", got)
		}
		if got := h.blobString(t, "jobs/"+jobID+"/input/revision-2/remarks/1.png"); got != attachment {
			t.Fatalf("input blob changed: %q", got)
		}

		// The input is still served to the worker.
		code, b := h.wreq(http.MethodGet,
			fmt.Sprintf("/api/worker/jobs/%s/input/%s?epoch=%d", jobID, urlQueryEscape("revision-2/remarks/1.png"), asn.Epoch),
			wtoken, nil)
		if code != http.StatusOK || string(b) != attachment {
			t.Fatalf("worker input: %d %q", code, b)
		}

		// The crop row lives in its own namespace.
		files, err := h.st.FilesByJob(context.Background(), jobID)
		if err != nil {
			t.Fatal(err)
		}
		var crop, input *store.File
		for i := range files {
			f := files[i]
			switch {
			case f.Kind == store.FileCrop:
				cp := f
				crop = &cp
			case f.Kind == store.FileInput && f.Path == "input/revision-2/remarks/1.png":
				ip := f
				input = &ip
			}
		}
		if crop == nil || crop.Path != "revision-crops/2/1.png" || crop.BlobKey != cropKey {
			t.Fatalf("crop row = %+v", crop)
		}
		if input == nil {
			t.Fatalf("input row not found: %+v", files)
		}

		// A failed registration (no such remark) rolls the orphan blob back
		// and must leave the input blob untouched.
		code, b = h.wreq(http.MethodPut,
			fmt.Sprintf("/api/worker/jobs/%s/input/revision/2/remarks/2?epoch=%d", jobID, asn.Epoch),
			wtoken, strings.NewReader("ORPHAN"))
		if code != http.StatusConflict {
			t.Fatalf("crop idx 2: %d %s, want 409", code, b)
		}
		if got := h.blobString(t, inputKey); got != attachment {
			t.Fatalf("failed crop upload damaged the input blob: %q", got)
		}
		if _, _, err := h.bl.Open(t.Context(), "jobs/"+jobID+"/revision-crops/2/2.png"); err == nil {
			t.Fatal("orphan crop blob was not cleaned up")
		}
	})

	t.Run("initial input revision-2/remarks/1.png", func(t *testing.T) {
		h := newWorkerHarness(t)
		wtoken := h.seedWorker("pc-1")
		cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
		jobID := h.submitJobWithFiles(cookie, "Реферат по истории", map[string]string{
			"задание.txt":              "TASK CONTENT",
			"revision-2/remarks/1.png": "INITIAL INPUT",
		})
		asn := revisionCropJob(t, h, wtoken, cookie, jobID, "")

		initialKey := "jobs/" + jobID + "/input/revision-2/remarks/1.png"
		if got := h.blobString(t, initialKey); got != "INITIAL INPUT" {
			t.Fatalf("initial input = %q", got)
		}
		code, b := h.wreq(http.MethodPut,
			fmt.Sprintf("/api/worker/jobs/%s/input/revision/2/remarks/1?epoch=%d", jobID, asn.Epoch),
			wtoken, strings.NewReader("CROP"))
		if code != http.StatusNoContent {
			t.Fatalf("crop: %d %s", code, b)
		}
		if got := h.blobString(t, initialKey); got != "INITIAL INPUT" {
			t.Fatalf("crop overwrote the initial input: %q", got)
		}
		if got := h.blobString(t, "jobs/"+jobID+"/revision-crops/2/1.png"); got != "CROP" {
			t.Fatalf("crop blob = %q", got)
		}
	})
}
