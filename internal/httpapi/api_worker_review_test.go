package httpapi_test

// Тесты на исправления ревью PR 3 (см. задание): stage_started_at и таймаут
// этапа, окно статусов, повторный commit draft, очистка снимка на повторе,
// приоритет отмены, continue без истечения lease, валидация idx при commit.

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckydiss/studlance_ai/internal/store"
)

// ---------- локальные хелперы (префикс rev, чтобы не пересекаться с другими файлами) ----------

// revStepStates возвращает состояния шагов окна статуса.
func revStepStates(j tClientJob) []string {
	out := make([]string, 0, len(j.StatusSteps))
	for _, s := range j.StatusSteps {
		out = append(out, s.State)
	}
	return out
}

// revExpectSteps сравнивает состояния шагов с ожидаемыми.
func revExpectSteps(t *testing.T, j tClientJob, want ...string) {
	t.Helper()
	got := revStepStates(j)
	if len(got) != len(want) {
		t.Fatalf("status steps: got %v want %v (%+v)", got, want, j.StatusSteps)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status steps: got %v want %v (%+v)", got, want, j.StatusSteps)
		}
	}
}

// revDoc — один документ снимка с одной страницей для commit.
func revDoc(idx int, filePath string) map[string]interface{} {
	return map[string]interface{}{
		"idx": idx, "title": "Пояснительная записка", "kind": "Word",
		"file_path": filePath, "page_count": 1,
		"pages": []map[string]interface{}{{"page": 1, "width": 100, "height": 140}},
	}
}

// revCommitRaw шлёт commit снимка и возвращает код и тело без проверок.
func revCommitRaw(h *wkHarness, wtoken, jobID string, epoch int, snap string, docs []map[string]interface{}) (int, []byte) {
	h.t.Helper()
	body := map[string]interface{}{"epoch": epoch, "documents": docs}
	if snap != "draft" {
		body["verification"] = map[string]interface{}{"found": 0, "fixed": 0, "remaining": []map[string]interface{}{}}
	}
	return h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/snapshot/"+snap+"/commit", wtoken, jsonReader(body))
}

// revPutFile грузит один out/-файл снимка.
func revPutFile(h *wkHarness, wtoken, jobID string, epoch int, snap, path, content string) {
	h.t.Helper()
	code, b := h.wreq(http.MethodPut,
		fmt.Sprintf("/api/worker/jobs/%s/snapshot/%s/files?epoch=%d&path=%s", jobID, snap, epoch, path),
		wtoken, strings.NewReader(content))
	if code != http.StatusNoContent {
		h.t.Fatalf("put file %s: %d %s", path, code, b)
	}
}

// revPutPageThumb грузит страницу 1 и её мини-копию для документа idx.
func revPutPageThumb(h *wkHarness, wtoken, jobID string, epoch int, snap string, idx int) {
	h.t.Helper()
	code, b := h.wreq(http.MethodPut,
		fmt.Sprintf("/api/worker/jobs/%s/snapshot/%s/pages/%d/1?epoch=%d", jobID, snap, idx, epoch),
		wtoken, strings.NewReader("PNG-PAGE"))
	if code != http.StatusNoContent {
		h.t.Fatalf("put page idx=%d: %d %s", idx, code, b)
	}
	code, b = h.wreq(http.MethodPut,
		fmt.Sprintf("/api/worker/jobs/%s/snapshot/%s/thumbs/%d/1?epoch=%d", jobID, snap, idx, epoch),
		wtoken, strings.NewReader("PNG-THUMB"))
	if code != http.StatusNoContent {
		h.t.Fatalf("put thumb idx=%d: %d %s", idx, code, b)
	}
}

// revClockAheadOfRealNow двигает fake-часы вперёд за реальное время. Часы
// харнесса стартуют в 2025-01-01, а HTTP-слой (s.now) использует настоящее
// время: без этого шага условие «этап draft идёт больше минуты» в StatusSteps
// срабатывало бы сразу после claim и шаг «Делаем работу» начинался бы раньше
// времени.
func revClockAheadOfRealNow(h *wkHarness) {
	h.t.Helper()
	h.clock.Add(time.Now().UTC().Add(2 * time.Hour).Sub(h.clock.Now()))
}

// revCancelRequest отменяет заказ от имени клиента.
func revCancelRequest(h *wkHarness, cookie, jobID string) {
	h.t.Helper()
	code, b := h.creq(http.MethodPost, "/api/client/jobs/"+jobID+"/cancel", cookie, nil)
	if code != http.StatusOK {
		h.t.Fatalf("cancel: %d %s", code, b)
	}
}

// ---------- тесты ----------

// 1. Таймаут этапа не считает время в needs_input: после выдачи ответа отсчёт
// начинается заново.
func TestRevTimeoutSkipsNeedsInput(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Курсовая работа по деталям машин, вариант 14")
	asn := h.claim(wtoken)
	if asn.Action != "start" || asn.Stage != "draft" {
		t.Fatalf("claim: %+v", asn)
	}

	code, b := h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/question", wtoken, jsonReader(map[string]interface{}{
		"epoch": asn.Epoch, "text": "Какой вариант методички?",
	}))
	if code != http.StatusNoContent {
		t.Fatalf("question: %d %s", code, b)
	}

	// 4 часа в needs_input не засчитываются в таймаут этапа.
	h.clock.Add(4 * time.Hour)
	code, b = h.creq(http.MethodPost, "/api/client/jobs/"+jobID+"/answer", cookie, jsonReader(map[string]string{"text": "Вариант 14"}))
	if code != http.StatusOK {
		t.Fatalf("answer: %d %s", code, b)
	}

	asn2 := h.claim(wtoken)
	if asn2.Action != "answer" {
		t.Fatalf("claim after answer: %+v", asn2)
	}

	// Ответ только что выдан: отсчёт этапа идёт заново, sweep ничего не трогает.
	if err := h.queue.Sweep(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	j, err := h.st.JobByID(t.Context(), jobID)
	if err != nil || j.Status != store.StatusRunning || j.LeaseEpoch != int64(asn2.Epoch) {
		t.Fatalf("job after answer: %+v %v (epoch %d)", j, err, asn2.Epoch)
	}

	// 3 ч 1 мин работы после выдачи ответа → таймаут этапа, авто-повтор.
	h.clock.Add(3*time.Hour + time.Minute)
	if err := h.queue.Sweep(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	j, err = h.st.JobByID(t.Context(), jobID)
	if err != nil || j.Status != store.StatusQueued || j.Attempt != 1 {
		t.Fatalf("job after stage timeout: %+v %v", j, err)
	}
	// Ошибка записывается, когда заказ окончательно падает; после авто-повтора
	// (attempt=1) error ещё пуст — он появляется на втором таймауте.
	if j.Error != nil {
		t.Fatalf("error после авто-повтора должен быть пуст, got %q", *j.Error)
	}

	asn3 := h.claim(wtoken)
	if asn3.Action != "start" || asn3.Attempt != 1 {
		t.Fatalf("re-claim: %+v", asn3)
	}
	h.clock.Add(3*time.Hour + time.Minute)
	if err := h.queue.Sweep(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	j, err = h.st.JobByID(t.Context(), jobID)
	if err != nil || j.Status != store.StatusFailed || j.Error == nil ||
		!strings.Contains(*j.Error, "превышен таймаут этапа") {
		t.Fatalf("job after second timeout: %+v %v", j, err)
	}
}

// 2. Окно статуса: активен последний начавшийся шаг.
func TestRevStatusStepsProgression(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Лабораторная по схемотехнике, нужны расчёты")

	revExpectSteps(t, h.clientJob(cookie, jobID), "active", "pending", "pending", "pending", "pending")

	revClockAheadOfRealNow(h)
	asn := h.claim(wtoken)
	if asn.Action != "start" || asn.Stage != "draft" {
		t.Fatalf("claim: %+v", asn)
	}
	// Есть stage_started draft → «Разбираем задание и методичку» active.
	revExpectSteps(t, h.clientJob(cookie, jobID), "done", "active", "pending", "pending", "pending")

	// Первый шаг трейса codex/draft → «Делаем работу» active.
	h.createRunWithSteps(wtoken, jobID, asn, "codex")
	revExpectSteps(t, h.clientJob(cookie, jobID), "done", "done", "active", "pending", "pending")

	// commit draft → verify → «Оформляем по требованиям методички» active.
	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "draft", "DRAFT DOC")
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "draft", "Готовая записка")
	revExpectSteps(t, h.clientJob(cookie, jobID), "done", "done", "done", "active", "pending")

	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "v1", "FINAL DOC")
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "v1", "")
	v1 := 1
	h.finish(wtoken, jobID, asn.Epoch, "ok", &v1, nil)

	j := h.clientJob(cookie, jobID)
	revExpectSteps(t, j, "done", "done", "done", "done", "done")
	if got := j.StatusSteps[len(j.StatusSteps)-1].Title; got != "Готово — версия 1" {
		t.Fatalf("последний шаг: %q", got)
	}
}

// 3. Повторный commit draft после успешного — 204 no-op; загрузка файлов в
// draft после commit — 409; дальше заказ идёт по обычному пути.
func TestRevDraftCommitRetry(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Реферат по истории, 15 страниц текста")
	asn := h.claim(wtoken)

	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "draft", "DRAFT DOC")
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "draft", "Готовая записка")

	// Повторный commit draft (потерянный ответ): уже зафиксирован, заказ на
	// verify — успех без изменений.
	code, b := revCommitRaw(h, wtoken, jobID, asn.Epoch, "draft", []map[string]interface{}{revDoc(0, "записка.docx")})
	if code != http.StatusNoContent {
		t.Fatalf("повторный commit draft: %d want 204 (%s)", code, b)
	}

	// А вот новые файлы в draft после commit не принимаются.
	code, b = h.wreq(http.MethodPut,
		fmt.Sprintf("/api/worker/jobs/%s/snapshot/draft/files?epoch=%d&path=ещё.docx", jobID, asn.Epoch),
		wtoken, strings.NewReader("x"))
	if code != http.StatusConflict {
		t.Fatalf("put draft file после commit: %d want 409 (%s)", code, b)
	}

	// Полный путь v1 работает.
	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "v1", "FINAL DOC")
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "v1", "")
	v1 := 1
	h.finish(wtoken, jobID, asn.Epoch, "ok", &v1, nil)
	j := h.clientJob(cookie, jobID)
	if j.ClientStatus != "done" || j.CurrentVersion != 1 {
		t.Fatalf("client: %+v", j)
	}
}

// 4. На повторной выдаче (start) снимок этапа очищается: файлы провалившейся
// попытки не попадают в bundle версии.
func TestRevSnapshotPurgedOnRetry(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Курсовая по теплотехнике с расчётом цикла")
	asn := h.claim(wtoken)

	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "draft", "DRAFT DOC")
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "draft", "Готовая записка")

	// Первая попытка verify: два файла, commit v1 с двумя документами.
	revPutFile(h, wtoken, jobID, asn.Epoch, "v1", "записка.docx", "FINAL DOC")
	revPutFile(h, wtoken, jobID, asn.Epoch, "v1", "старый.docx", "STALE DOC")
	revPutPageThumb(h, wtoken, jobID, asn.Epoch, "v1", 0)
	revPutPageThumb(h, wtoken, jobID, asn.Epoch, "v1", 1)
	code, b := revCommitRaw(h, wtoken, jobID, asn.Epoch, "v1",
		[]map[string]interface{}{revDoc(0, "записка.docx"), revDoc(1, "старый.docx")})
	if code != http.StatusNoContent {
		t.Fatalf("commit v1: %d %s", code, b)
	}
	h.finish(wtoken, jobID, asn.Epoch, "failed", nil, strPtr("claude завершился с кодом 1"))

	// Авто-повтор: выдача start очищает снимок этапа verify.
	asn2 := h.claim(wtoken)
	if asn2.Action != "start" || asn2.Attempt != 1 || asn2.Stage != "verify" {
		t.Fatalf("retry claim: %+v", asn2)
	}

	// Вторая попытка: только записка.docx.
	revPutFile(h, wtoken, jobID, asn2.Epoch, "v1", "записка.docx", "FINAL DOC")
	revPutPageThumb(h, wtoken, jobID, asn2.Epoch, "v1", 0)
	code, b = revCommitRaw(h, wtoken, jobID, asn2.Epoch, "v1", []map[string]interface{}{revDoc(0, "записка.docx")})
	if code != http.StatusNoContent {
		t.Fatalf("commit v1 retry: %d %s", code, b)
	}
	v1 := 1
	h.finish(wtoken, jobID, asn2.Epoch, "ok", &v1, nil)

	// В bundle версии только файл второй попытки.
	code, b = h.creq(http.MethodGet, "/api/client/jobs/"+jobID+"/versions/1/bundle.zip", cookie, nil)
	if code != http.StatusOK {
		t.Fatalf("bundle: %d %s", code, b)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("bundle zip: %v", err)
	}
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if len(names) != 1 || names[0] != "записка.docx" {
		t.Fatalf("bundle entries: %v", names)
	}

	// Файл провалившейся попытки не отдаётся.
	code, b = h.creq(http.MethodGet, "/api/client/jobs/"+jobID+"/versions/1/files/старый.docx", cookie, nil)
	if code != http.StatusNotFound {
		t.Fatalf("stale file: %d want 404 (%s)", code, b)
	}
}

// 5. Запрошенная отмена сильнее сбоя, вопроса и таймаута этапа.
func TestRevCancelBeatsFailure(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)

	// (a) cancel → finish failed → canceled.
	jobA := h.submitJob(cookie, "Программа на Паскале для учёта склада")
	asnA := h.claim(wtoken)
	revCancelRequest(h, cookie, jobA)
	h.finish(wtoken, jobA, asnA.Epoch, "failed", nil, strPtr("codex упал"))
	j, err := h.st.JobByID(t.Context(), jobA)
	if err != nil || j.Status != store.StatusCanceled || j.FinishedAt == nil {
		t.Fatalf("job A: %+v %v", j, err)
	}
	if cj := h.clientJob(cookie, jobA); cj.ClientStatus != "canceled" {
		t.Fatalf("job A client: %q", cj.ClientStatus)
	}

	// (b) cancel → question → canceled (не needs_input).
	jobB := h.submitJob(cookie, "Расчёт балки на прочность по Сопромату")
	asnB := h.claim(wtoken)
	revCancelRequest(h, cookie, jobB)
	code, b := h.wreq(http.MethodPost, "/api/worker/jobs/"+jobB+"/question", wtoken, jsonReader(map[string]interface{}{
		"epoch": asnB.Epoch, "text": "Какая методичка?",
	}))
	if code != http.StatusNoContent {
		t.Fatalf("question: %d %s", code, b)
	}
	j, err = h.st.JobByID(t.Context(), jobB)
	if err != nil || j.Status != store.StatusCanceled {
		t.Fatalf("job B: %+v %v", j, err)
	}

	// (c) cancel → таймаут этапа из Sweep → canceled (не queued/failed).
	jobC := h.submitJob(cookie, "Отчёт по практике в строительной фирме")
	_ = h.claim(wtoken)
	revCancelRequest(h, cookie, jobC)
	h.clock.Add(3*time.Hour + time.Minute)
	if err := h.queue.Sweep(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	j, err = h.st.JobByID(t.Context(), jobC)
	if err != nil || j.Status != store.StatusCanceled {
		t.Fatalf("job C: %+v %v", j, err)
	}
}

// 6. Воркер с running-заказом получает его как continue сразу, без истечения
// lease; старый epoch умирает.
func TestRevContinueWithoutLeaseExpiry(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Чертёж валика по КОМПАС с размерами")
	asn := h.claim(wtoken)
	if asn.Action != "start" {
		t.Fatalf("claim: %+v", asn)
	}

	asn2 := h.claim(wtoken)
	if asn2.JobID != jobID || asn2.Action != "continue" || asn2.Epoch != asn.Epoch+1 {
		t.Fatalf("continue: %+v (первый epoch %d)", asn2, asn.Epoch)
	}

	code, b := h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/heartbeat", wtoken, jsonReader(map[string]interface{}{
		"epoch": asn.Epoch,
	}))
	if code != http.StatusConflict || !strings.Contains(string(b), "stale_lease") {
		t.Fatalf("heartbeat со старым epoch: %d %s", code, b)
	}
}

// 7. commit с дублирующимся или отрицательным idx документа — 400, не 500.
func TestRevCommitBadIdx(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Расчётно-графическая по матанализу, ряды")
	asn := h.claim(wtoken)
	h.uploadSnapshot(wtoken, jobID, asn.Epoch, "draft", "DRAFT DOC")

	code, b := revCommitRaw(h, wtoken, jobID, asn.Epoch, "draft",
		[]map[string]interface{}{revDoc(0, "записка.docx"), revDoc(0, "ещё.docx")})
	if code != http.StatusBadRequest {
		t.Fatalf("commit с повторным idx: %d want 400 (%s)", code, b)
	}

	code, b = revCommitRaw(h, wtoken, jobID, asn.Epoch, "draft",
		[]map[string]interface{}{revDoc(-1, "записка.docx")})
	if code != http.StatusBadRequest {
		t.Fatalf("commit с idx=-1: %d want 400 (%s)", code, b)
	}

	// Нормальный commit после этого проходит.
	h.commitSnapshot(wtoken, jobID, asn.Epoch, "draft", "Готовая записка")
}
