//go:build e2e

package e2e

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luckydiss/studlance_ai/internal/httpapi"
)

// Обычный заказ: done v1, страницы и мини-копии отдаются клиенту.
func TestNormalOrder(t *testing.T) {
	h := newHarness(t)
	h.startWorker()

	jobID := h.newJob("Сделай практическую работу: расчёт балки по данным из файла")
	h.waitClientStatus(jobID, "done", 60*time.Second)

	d := h.clientJob(jobID)
	if d.CurrentVersion != 1 {
		t.Fatalf("current_version = %d, want 1", d.CurrentVersion)
	}
	if len(d.Versions) != 1 || len(d.Versions[0].Documents) < 2 {
		t.Fatalf("versions = %+v", d.Versions)
	}
	doc := d.Versions[0].Documents[0]
	if doc.PageCount < 2 {
		t.Fatalf("page_count = %d, want >= 2", doc.PageCount)
	}

	// Pages and thumbs are served to the client.
	var pages httpapi.PageList
	if code := h.doJSON(http.MethodGet,
		fmt.Sprintf("/api/client/jobs/%s/versions/1/documents/%s/pages", jobID, doc.Id),
		h.clientCookie, nil, &pages); code != http.StatusOK {
		t.Fatalf("pages: %d", code)
	}
	if len(pages.Pages) != doc.PageCount {
		t.Fatalf("pages = %d, page_count = %d", len(pages.Pages), doc.PageCount)
	}
	resp := h.doRaw(http.MethodGet,
		fmt.Sprintf("/api/client/jobs/%s/versions/1/pages/%s/1.png", jobID, doc.Id), h.clientCookie, nil, "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/png") {
		t.Fatalf("page png: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	resp2 := h.doRaw(http.MethodGet,
		fmt.Sprintf("/api/client/jobs/%s/versions/1/thumbs/%s/1.png", jobID, doc.Id), h.clientCookie, nil, "")
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("thumb: %d", resp2.StatusCode)
	}

	// bundle.zip opens and contains the Cyrillic-named documents.
	resp3 := h.doRaw(http.MethodGet, fmt.Sprintf("/api/client/jobs/%s/versions/1/bundle.zip", jobID), h.clientCookie, nil, "")
	defer func() { _ = resp3.Body.Close() }()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("bundle: %d", resp3.StatusCode)
	}
	raw, err := io.ReadAll(resp3.Body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("bundle is not a zip: %v", err)
	}
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if !containsName(names, "Пояснительная записка.docx") || !containsName(names, "Расчёт.xlsx") {
		t.Fatalf("bundle names = %v", names)
	}

	// Admin sees both agent runs and the verification.
	aj := h.adminJob(jobID)
	if len(aj.AgentRuns) != 2 {
		t.Fatalf("agent_runs = %d, want 2", len(aj.AgentRuns))
	}
	if aj.Verification == nil || aj.Verification.Found != 3 {
		t.Fatalf("verification = %+v", aj.Verification)
	}
	if aj.Draft == nil || len(aj.Draft.Documents) < 2 {
		t.Fatalf("draft = %+v", aj.Draft)
	}
}

// #ask: вопрос → needs_input → ответ клиента → action answer → done.
func TestAskFlow(t *testing.T) {
	h := newHarness(t)
	h.startWorker()

	jobID := h.newJob("Сделай практическую работу #ask")
	h.waitClientStatus(jobID, "needs_input", 60*time.Second)
	d := h.clientJob(jobID)
	if d.Question == nil || *d.Question == "" {
		t.Fatalf("no question in detail")
	}
	if code := h.doJSON(http.MethodPost, "/api/client/jobs/"+jobID+"/answer", h.clientCookie,
		map[string]string{"text": "Нагрузка q = 10 кН/м, вариант 14"}, nil); code != http.StatusOK {
		t.Fatalf("answer: %d", code)
	}
	h.waitClientStatus(jobID, "done", 60*time.Second)
	if d := h.clientJob(jobID); d.CurrentVersion != 1 {
		t.Fatalf("current_version = %d", d.CurrentVersion)
	}
	// The agent asked once: the answer run finished the draft.
	aj := h.adminJob(jobID)
	for _, r := range aj.AgentRuns {
		if r.Outcome != nil && *r.Outcome == "question" {
			return
		}
	}
	t.Fatalf("no run with outcome=question")
}

// #fail-once: авто-повтор → done.
func TestFailOnce(t *testing.T) {
	h := newHarness(t)
	h.startWorker()

	jobID := h.newJob("Сделай практическую работу #fail-once")
	h.waitClientStatus(jobID, "done", 90*time.Second)
	aj := h.adminJob(jobID)
	var codexRuns int
	for _, r := range aj.AgentRuns {
		if r.Agent == "codex" {
			codexRuns++
		}
	}
	if codexRuns != 2 {
		t.Fatalf("codex runs = %d, want 2 (авто-повтор)", codexRuns)
	}
}

// #fail-verify: failed + needs_attention, клиент видит «Задерживается».
func TestFailVerify(t *testing.T) {
	h := newHarness(t)
	h.startWorker()

	jobID := h.newJob("Сделай практическую работу #fail-verify")
	h.waitClientStatus(jobID, "delayed", 90*time.Second)
	aj := h.adminJob(jobID)
	if aj.Status != "failed" || !aj.NeedsAttention {
		t.Fatalf("status = %s needs_attention = %v", aj.Status, aj.NeedsAttention)
	}
}

// #hang + отмена клиентом → canceled, процесс агента убит.
func TestHangCancel(t *testing.T) {
	h := newHarness(t)
	h.startWorker()

	jobID := h.newJob("Сделай практическую работу #hang")
	h.waitClientStatus(jobID, "in_progress", 30*time.Second)

	pidFile := filepath.Join(h.workDir, jobID, ".fakeagent", "hang.pid")
	h.waitFor("hang.pid", 15*time.Second, func() bool {
		_, err := os.Stat(pidFile)
		return err == nil
	})
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}

	if code := h.doJSON(http.MethodPost, "/api/client/jobs/"+jobID+"/cancel", h.clientCookie, nil, nil); code != http.StatusOK {
		t.Fatalf("cancel: %d", code)
	}
	h.waitClientStatus(jobID, "canceled", 30*time.Second)
	h.waitFor("agent process dead", 10*time.Second, func() bool { return !processAlive(pid) })
}

// #no-pdf: документы без превью (или со сконвертированным), заказ done.
func TestNoPDF(t *testing.T) {
	h := newHarness(t)
	h.startWorker()

	jobID := h.newJob("Сделай практическую работу #no-pdf")
	h.waitClientStatus(jobID, "done", 90*time.Second)
	d := h.clientJob(jobID)
	if len(d.Versions) != 1 || len(d.Versions[0].Documents) < 2 {
		t.Fatalf("versions = %+v", d.Versions)
	}
	// On a machine without Office the documents have no pages; where Word/Excel
	// exist the worker converts previews itself. Both are acceptable.
	for _, doc := range d.Versions[0].Documents {
		if doc.PageCount > 0 {
			var pages httpapi.PageList
			if code := h.doJSON(http.MethodGet,
				fmt.Sprintf("/api/client/jobs/%s/versions/1/documents/%s/pages", jobID, doc.Id),
				h.clientCookie, nil, &pages); code != http.StatusOK {
				t.Fatalf("converted pages: %d", code)
			}
		}
	}
}

// Доработка с двумя замечаниями: вырезки, REVISION-2.md, done v2, changed_boxes.
func TestRevision(t *testing.T) {
	h := newHarness(t)
	h.startWorker()

	jobID := h.newJob("Сделай практическую работу: расчёт балки")
	h.waitClientStatus(jobID, "done", 60*time.Second)
	d := h.clientJob(jobID)
	docID := d.Versions[0].Documents[0].Id

	data := fmt.Sprintf(`{"comment": "Поправьте, пожалуйста", "remarks": [
		{"document_id": %q, "page": 1, "x": 0.1, "y": 0.1, "w": 0.4, "h": 0.2, "text": "Уточните расчёт прогиба"},
		{"document_id": %q, "page": 2, "x": 0.2, "y": 0.3, "w": 0.3, "h": 0.2, "text": "Добавьте эпюру моментов"}
	]}`, docID, docID)
	body, ct := revisionBody(t, data, map[string]string{"дополнительные данные.txt": "l = 2 м"})
	resp := h.doRaw(http.MethodPost, "/api/client/jobs/"+jobID+"/revisions", h.clientCookie, body, ct)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("revisions: %d", resp.StatusCode)
	}

	h.waitClientStatus(jobID, "done", 90*time.Second)
	d = h.clientJob(jobID)
	if d.CurrentVersion != 2 {
		t.Fatalf("current_version = %d, want 2", d.CurrentVersion)
	}

	// REVISION-2.md по формату и вырезки на месте (рабочая папка воркера).
	raw, err := os.ReadFile(filepath.Join(h.workDir, jobID, "REVISION-2.md"))
	if err != nil {
		t.Fatalf("REVISION-2.md: %v", err)
	}
	text := string(raw)
	for _, want := range []string{"# Доработка — версия 2", "Уточните расчёт прогиба", "стр. 1", "Вырезка: input/revision-2/remarks/1.png", "input/revision-2/дополнительные данные.txt"} {
		if !strings.Contains(text, want) {
			t.Fatalf("REVISION-2.md missing %q:\n%s", want, text)
		}
	}
	for _, idx := range []int{1, 2} {
		crop := filepath.Join(h.workDir, jobID, "input", "revision-2", "remarks", fmt.Sprintf("%d.png", idx))
		if fi, err := os.Stat(crop); err != nil || fi.Size() == 0 {
			t.Fatalf("crop %d missing or empty: %v", idx, err)
		}
	}

	// changed_boxes непустые на изменённых страницах версии 2. Идентификаторы
	// документов перевыдаются при commit — берём документ из v2.
	d = h.clientJob(jobID)
	var doc2 *httpapi.Document
	for i := range d.Versions[1].Documents {
		if d.Versions[1].Documents[i].FilePath == "Пояснительная записка.docx" {
			doc2 = &d.Versions[1].Documents[i]
		}
	}
	if doc2 == nil {
		t.Fatalf("нет Пояснительной записки в v2: %+v", d.Versions[1].Documents)
	}
	var pages httpapi.PageList
	if code := h.doJSON(http.MethodGet,
		fmt.Sprintf("/api/client/jobs/%s/versions/2/documents/%s/pages", jobID, doc2.Id),
		h.clientCookie, nil, &pages); code != http.StatusOK {
		t.Fatalf("pages v2: %d", code)
	}
	if len(pages.Pages) < 2 || len(pages.Pages[0].ChangedBoxes) == 0 {
		t.Fatalf("page 1 of v2 has no changed_boxes: %+v", pages.Pages)
	}
}

// Убить воркер посреди этапа и запустить снова → continue → done.
func TestKillWorkerContinue(t *testing.T) {
	h := newHarness(t)
	h.startWorker()

	jobID := h.newJob("Сделай практическую работу #slow")
	h.waitFor("draft run started", 30*time.Second, func() bool {
		aj := h.adminJob(jobID)
		for _, r := range aj.AgentRuns {
			if r.Agent == "codex" {
				return true
			}
		}
		return false
	})
	threadID, _ := h.adminJob(jobID).State["codex_thread_id"].(string)
	if threadID == "" {
		t.Fatalf("codex_thread_id not posted yet")
	}

	// Убиваем воркер (Ctrl+C): без finish, заказ продолжится как continue.
	h.stopWorker()
	h.startWorker()

	h.waitClientStatus(jobID, "done", 90*time.Second)
	after, _ := h.adminJob(jobID).State["codex_thread_id"].(string)
	if after != threadID {
		t.Fatalf("thread id changed: %s -> %s (resume не сработал)", threadID, after)
	}
	var codexRuns int
	for _, r := range h.adminJob(jobID).AgentRuns {
		if r.Agent == "codex" {
			codexRuns++
		}
	}
	if codexRuns != 2 {
		t.Fatalf("codex runs = %d, want 2 (прерванный + continue)", codexRuns)
	}
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// #fail-verify-once: verify падает один раз → авто-повтор с НОВОЙ сессией
// claude → done v1 (ревью, п.1: свежий session id и есть ожидаемый).
func TestFailVerifyOnce(t *testing.T) {
	h := newHarness(t)
	h.startWorker()

	jobID := h.newJob("Сделай практическую работу #fail-verify-once")
	h.waitClientStatus(jobID, "done", 90*time.Second)
	aj := h.adminJob(jobID)
	var claudeRuns int
	for _, r := range aj.AgentRuns {
		if r.Agent == "claude" {
			claudeRuns++
		}
	}
	if claudeRuns != 2 {
		t.Fatalf("claude runs = %d, want 2 (падение + авто-повтор)", claudeRuns)
	}
	if aj.CurrentVersion != 1 {
		t.Fatalf("current_version = %d", aj.CurrentVersion)
	}
}

// Admin retry после исчерпанных падений verify: failed → retry → claim →
// verify проходит с новой сессией claude (ревью, п.1).
func TestAdminRetryAfterFailVerify(t *testing.T) {
	h := newHarness(t)
	h.startWorker()

	jobID := h.newJob("Сделай практическую работу #fail-verify-twice")
	h.waitClientStatus(jobID, "delayed", 90*time.Second)
	if aj := h.adminJob(jobID); aj.Status != "failed" || !aj.NeedsAttention {
		t.Fatalf("status = %s needs_attention = %v", aj.Status, aj.NeedsAttention)
	}

	if code := h.doJSON(http.MethodPost, "/api/admin/jobs/"+jobID+"/retry", h.adminCookie, nil, nil); code != http.StatusOK {
		t.Fatalf("admin retry: %d", code)
	}
	h.waitClientStatus(jobID, "done", 90*time.Second)
	aj := h.adminJob(jobID)
	var claudeRuns int
	for _, r := range aj.AgentRuns {
		if r.Agent == "claude" {
			claudeRuns++
		}
	}
	if claudeRuns != 3 {
		t.Fatalf("claude runs = %d, want 3 (2 падения + retry)", claudeRuns)
	}
}

// #longline: строка 10 МБ в JSONL не вешает и не валит этап (ревью, п.2).
func TestLongLine(t *testing.T) {
	h := newHarness(t)
	h.startWorker()

	jobID := h.newJob("Сделай практическую работу #longline")
	h.waitClientStatus(jobID, "done", 90*time.Second)

	// Сырой лог содержит длинную строку целиком.
	aj := h.adminJob(jobID)
	var runID string
	for _, r := range aj.AgentRuns {
		if r.Agent == "codex" {
			runID = r.Id
		}
	}
	if runID == "" {
		t.Fatalf("нет codex-рана")
	}
	resp := h.doRaw(http.MethodGet, fmt.Sprintf("/api/admin/jobs/%s/runs/%s/log", jobID, runID), h.adminCookie, nil, "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("log: %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 10_000_000 {
		t.Fatalf("лог %d байт, ждали > 10 МБ", len(raw))
	}
	if !bytes.Contains(raw, []byte("aggregated_output")) {
		t.Fatalf("в логе нет длинной строки")
	}
}

// Сеть не должна останавливать агента (ревью, п.5): API воркера «лежит» 3 с
// посреди draft — агент всё равно заканчивает работу, шаги доходят после.
func TestServerOutage(t *testing.T) {
	h := newHarnessGate(t, true)
	h.startWorker()

	jobID := h.newJob("Сделай практическую работу #slow")
	h.waitFor("draft run started", 30*time.Second, func() bool {
		for _, r := range h.adminJob(jobID).AgentRuns {
			if r.Agent == "codex" {
				return true
			}
		}
		return false
	})

	// «Ложим» API воркера: все /api/worker/* запросы висят.
	h.gate.Store(true)
	// Пока сеть лежит, агент заканчивает запись файлов (не заблокирован).
	if !h.poll(20*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(h.workDir, jobID, "manifest.json"))
		return err == nil
	}) {
		var listing strings.Builder
		_ = filepath.WalkDir(h.workDir, func(p string, d os.DirEntry, err error) error {
			if err == nil {
				listing.WriteString(p + "\n")
			}
			return nil
		})
		t.Fatalf("agent artifacts not written while API down\nworker logs:\n%s\nworkdir:\n%s", h.logs.String(), listing.String())
	}
	time.Sleep(500 * time.Millisecond)
	h.gate.Store(false)
	close(h.gateRelease)

	h.waitClientStatus(jobID, "done", 90*time.Second)
	// Шаги дошли после восстановления сети.
	aj := h.adminJob(jobID)
	for _, r := range aj.AgentRuns {
		if r.Agent == "codex" {
			var steps httpapi.TraceStepList
			if code := h.doJSON(http.MethodGet,
				fmt.Sprintf("/api/admin/jobs/%s/runs/%s/steps", jobID, r.Id), h.adminCookie, nil, &steps); code != http.StatusOK {
				t.Fatalf("steps: %d", code)
			}
			if len(steps.Steps) == 0 {
				t.Fatalf("шаги не дошли после восстановления сети")
			}
			return
		}
	}
	t.Fatalf("нет codex-рана")
}
