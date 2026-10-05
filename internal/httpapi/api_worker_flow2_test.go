package httpapi_test

// Сценарии 4–14 из задания на PR 3.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckydiss/studlance_ai/internal/store"
)

// 4. Сбой: finish failed → queued attempt=1 → claim start attempt=1 → второй
// сбой → failed + needs_attention + клиент видит delayed.
func TestWorkerFailureAutoRetry(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Курсовая по теплотехнике с расчётом цикла")
	asn := h.claim(wtoken)

	h.finish(wtoken, jobID, asn.Epoch, "failed", nil, strPtr("codex завершился с кодом 1"))

	j := h.clientJob(cookie, jobID)
	if j.ClientStatus != "in_progress" {
		t.Fatalf("после авто-повтора клиент должен видеть «Выполняем», got %q (%s)", j.ClientStatus, j.StatusText)
	}
	aj, err := h.st.JobByID(t.Context(), jobID)
	if err != nil || aj.Status != store.StatusQueued || aj.Attempt != 1 {
		t.Fatalf("job: %+v %v", aj, err)
	}

	asn2 := h.claim(wtoken)
	if asn2.Action != "start" || asn2.Attempt != 1 || asn2.Stage != "draft" {
		t.Fatalf("assignment: %+v", asn2)
	}
	h.finish(wtoken, jobID, asn2.Epoch, "failed", nil, strPtr("codex завершился с кодом 1"))

	j = h.clientJob(cookie, jobID)
	if j.ClientStatus != "delayed" {
		t.Fatalf("client status: %q", j.ClientStatus)
	}
	aj, err = h.st.JobByID(t.Context(), jobID)
	if err != nil || aj.Status != store.StatusFailed || !aj.NeedsAttention || aj.Error == nil {
		t.Fatalf("job: %+v %v", aj, err)
	}

	// Повтор админа будит очередь и возвращает заказ тому же воркеру.
	admin := h.seedUserHTTP("a@local", "pw", store.RoleAdmin)
	code, b := h.creq(http.MethodPost, "/api/admin/jobs/"+jobID+"/retry", admin, nil)
	if code != http.StatusOK {
		t.Fatalf("retry: %d %s", code, b)
	}
	asn3 := h.claim(wtoken)
	if asn3.Action != "start" || asn3.Attempt != 0 {
		t.Fatalf("after admin retry: %+v", asn3)
	}
}

// 5. Истёкший lease: сдвиг часов на 61 с → continue с epoch+1; чужой воркер
// заказ не получает.
func TestWorkerLeaseExpiryContinue(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	other := h.seedWorker("pc-2")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Лабораторная по физике, маятник и погрешности")
	asn := h.claim(wtoken)

	h.clock.Add(61 * time.Second)

	asn2 := h.claim(wtoken)
	if asn2.Action != "continue" || asn2.JobID != jobID || asn2.Epoch != asn.Epoch+1 {
		t.Fatalf("continue: %+v (epoch %d)", asn2, asn.Epoch)
	}
	if asn2.Stage != "draft" || asn2.Attempt != 0 {
		t.Fatalf("stage/attempt: %+v", asn2)
	}
	h.claimExpect204(other)
}

// 6. Запись со старым epoch → 409 stale_lease.
func TestWorkerStaleLease(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Расчёт балки на прочность по Сопромату")
	asn := h.claim(wtoken)
	code, b := h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/runs", wtoken, jsonReader(map[string]interface{}{
		"epoch": asn.Epoch, "agent": "codex", "stage": "draft", "version": 1, "attempt": 0,
	}))
	if code != http.StatusCreated {
		t.Fatalf("run: %d %s", code, b)
	}
	runID := decodeAs[map[string]string](t, b)["run_id"]

	// lease истёк, тот же воркер перезабирает заказ — epoch растёт.
	h.clock.Add(61 * time.Second)
	asn2 := h.claim(wtoken)
	stale := asn.Epoch
	fresh := asn2.Epoch

	expectStale := func(code int, b []byte, what string) {
		t.Helper()
		if code != http.StatusConflict {
			t.Fatalf("%s: %d want 409 (%s)", what, code, b)
		}
		if !strings.Contains(string(b), "stale_lease") {
			t.Fatalf("%s: body %s want stale_lease", what, b)
		}
	}

	code, b = h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/heartbeat", wtoken, jsonReader(map[string]interface{}{"epoch": stale}))
	expectStale(code, b, "heartbeat")

	code, b = h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/runs/"+runID+"/steps", wtoken, jsonReader(map[string]interface{}{
		"epoch": stale, "steps": []map[string]interface{}{{"seq": 3, "ts": time.Now().UTC(), "type": "message", "summary": "x"}},
	}))
	expectStale(code, b, "steps")

	code, b = h.wreq(http.MethodPut, fmt.Sprintf("/api/worker/jobs/%s/snapshot/draft/files?epoch=%d&path=x.txt", jobID, stale), wtoken, strings.NewReader("x"))
	expectStale(code, b, "snapshot file")

	code, b = h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/snapshot/draft/commit", wtoken, jsonReader(map[string]interface{}{
		"epoch": stale, "documents": []map[string]interface{}{{"idx": 0, "title": "t", "kind": "Word", "file_path": "x.txt", "page_count": 0, "pages": []interface{}{}}},
	}))
	expectStale(code, b, "commit")

	code, b = h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/finish", wtoken, jsonReader(map[string]interface{}{"epoch": stale, "outcome": "failed", "error": "x"}))
	expectStale(code, b, "finish")

	code, b = h.wreq(http.MethodGet, fmt.Sprintf("/api/worker/jobs/%s/input?epoch=%d", jobID, stale), wtoken, nil)
	expectStale(code, b, "input list")

	// Со свежим epoch heartbeat проходит.
	code, b = h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/heartbeat", wtoken, jsonReader(map[string]interface{}{"epoch": fresh}))
	if code != http.StatusOK {
		t.Fatalf("heartbeat fresh: %d %s", code, b)
	}
	if decodeAs[map[string]interface{}](t, b)["cancel"] != false {
		t.Fatalf("cancel flag: %s", b)
	}
}

// 7. Повтор пачки шагов с теми же seq не дублирует строки.
func TestWorkerStepsIdempotent(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	admin := h.seedUserHTTP("a@local", "pw", store.RoleAdmin)
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Чертёж валика по КОМПАС с размерами")
	asn := h.claim(wtoken)
	code, b := h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/runs", wtoken, jsonReader(map[string]interface{}{
		"epoch": asn.Epoch, "agent": "codex", "stage": "draft", "version": 1, "attempt": 0,
	}))
	if code != http.StatusCreated {
		t.Fatalf("run: %d %s", code, b)
	}
	runID := decodeAs[map[string]string](t, b)["run_id"]

	batch := map[string]interface{}{
		"epoch": asn.Epoch,
		"steps": []map[string]interface{}{
			{"seq": 1, "ts": time.Now().UTC(), "type": "message", "summary": "шаг 1"},
			{"seq": 2, "ts": time.Now().UTC(), "type": "message", "summary": "шаг 2"},
		},
	}
	for i := 0; i < 3; i++ {
		code, b = h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/runs/"+runID+"/steps", wtoken, jsonReader(batch))
		if code != http.StatusNoContent {
			t.Fatalf("steps: %d %s", code, b)
		}
	}

	code, b = h.creq(http.MethodGet, "/api/admin/jobs/"+jobID+"/runs/"+runID+"/steps", admin, nil)
	if code != http.StatusOK {
		t.Fatalf("admin steps: %d %s", code, b)
	}
	steps := decodeAs[struct {
		Steps []struct {
			Seq int `json:"seq"`
		} `json:"steps"`
	}](t, b)
	if len(steps.Steps) != 2 {
		t.Fatalf("дубликаты шагов: %+v", steps.Steps)
	}

	// Новые шаги публикуются в SSE админа как event: steps.
	stream := h.openAdminStream(admin, jobID)
	defer stream.Close()
	batch["steps"] = []map[string]interface{}{
		{"seq": 3, "ts": time.Now().UTC(), "type": "message", "summary": "шаг 3"},
	}
	code, b = h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/runs/"+runID+"/steps", wtoken, jsonReader(batch))
	if code != http.StatusNoContent {
		t.Fatalf("steps 3: %d %s", code, b)
	}
	if !stream.waitContains("event: steps", 3*time.Second) {
		t.Fatalf("нет event: steps в SSE: %s", stream.buf.String())
	}
	if !strings.Contains(stream.buf.String(), runID) {
		t.Fatalf("event steps без run_id: %s", stream.buf.String())
	}
}

// sseStream — открытый SSE-поток админа с накоплением прочитанного.
type sseStream struct {
	buf *safeBuffer
	r   io.ReadCloser
}

func (s *sseStream) Close() { _ = s.r.Close() }

func (s *sseStream) waitContains(sub string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(s.buf.String(), sub) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// openAdminStream открывает SSE-поток заказа для админа.
func (h *wkHarness) openAdminStream(adminCookie, jobID string) *sseStream {
	h.t.Helper()
	r, err := http.NewRequest(http.MethodGet, h.srv.URL+"/api/admin/jobs/"+jobID+"/stream", nil)
	if err != nil {
		h.t.Fatalf("stream req: %v", err)
	}
	r.Header.Set("Cookie", "sl_session="+adminCookie)
	resp, err := h.hc.Do(r) //nolint:bodyclose // closed by sseStream.Close
	if err != nil {
		h.t.Fatalf("stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		h.t.Fatalf("stream: %d", resp.StatusCode)
	}
	buf := &safeBuffer{}
	go func() {
		_, _ = io.Copy(buf, resp.Body)
	}()
	return &sseStream{buf: buf, r: resp.Body}
}

// 8. Отмена во время running: heartbeat → cancel: true → finish canceled.
func TestWorkerCancel(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Программа на Паскале для учёта склада")
	asn := h.claim(wtoken)

	// finish canceled без запроса отмены → 409.
	code, b := h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/finish", wtoken, jsonReader(map[string]interface{}{
		"epoch": asn.Epoch, "outcome": "canceled",
	}))
	if code != http.StatusConflict {
		t.Fatalf("finish canceled без отмены: %d %s", code, b)
	}

	code, b = h.creq(http.MethodPost, "/api/client/jobs/"+jobID+"/cancel", cookie, nil)
	if code != http.StatusOK {
		t.Fatalf("cancel: %d %s", code, b)
	}
	// Заказ ещё running, пока воркер не подтвердит.
	if j := h.clientJob(cookie, jobID); j.ClientStatus != "in_progress" {
		t.Fatalf("status during cancel: %q", j.ClientStatus)
	}

	code, b = h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/heartbeat", wtoken, jsonReader(map[string]interface{}{"epoch": asn.Epoch}))
	if code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", code, b)
	}
	if decodeAs[map[string]interface{}](t, b)["cancel"] != true {
		t.Fatalf("cancel flag: %s", b)
	}

	h.finish(wtoken, jobID, asn.Epoch, "canceled", nil, nil)
	if j := h.clientJob(cookie, jobID); j.ClientStatus != "canceled" {
		t.Fatalf("final status: %q", j.ClientStatus)
	}
}

// 9. Два параллельных claim при одном queued заказе — заказ получает ровно один.
func TestWorkerParallelClaims(t *testing.T) {
	h := newWorkerHarness(t)
	tokA := h.seedWorker("pc-1")
	tokB := h.seedWorker("pc-2")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Расчётно-графическая по матанализу, ряды")

	type result struct {
		code int
		asn  tAssignment
	}
	claimAsync := func(tok string, ch chan<- result) {
		code, b := h.wreq(http.MethodPost, "/api/worker/claim", tok, nil)
		r := result{code: code}
		if code == http.StatusOK {
			r.asn = decodeAs[tAssignment](t, b)
		}
		ch <- r
	}
	chA, chB := make(chan result, 1), make(chan result, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); claimAsync(tokA, chA) }()
	go func() { defer wg.Done(); claimAsync(tokB, chB) }()
	wg.Wait()
	ra, rb := <-chA, <-chB

	got := 0
	for _, r := range []result{ra, rb} {
		if r.code == http.StatusOK {
			got++
			if r.asn.JobID != jobID {
				t.Fatalf("wrong job: %+v", r.asn)
			}
		} else if r.code != http.StatusNoContent {
			t.Fatalf("claim code %d", r.code)
		}
	}
	if got != 1 {
		t.Fatalf("заказ получили %d воркеров, ожидался 1", got)
	}
}

// 10. Long-poll: submit будит ожидающий claim быстрее 2 с.
func TestWorkerLongPollWake(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)

	type result struct {
		code int
		asn  tAssignment
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		code, b := h.wreq(http.MethodPost, "/api/worker/claim", wtoken, nil)
		r := result{code: code}
		if code == http.StatusOK {
			r.asn = decodeAs[tAssignment](t, b)
		}
		done <- r
	}()

	time.Sleep(200 * time.Millisecond)
	jobID := h.submitJob(cookie, "Отчёт по практике в строительной фирме")

	select {
	case r := <-done:
		elapsed := time.Since(start)
		if r.code != http.StatusOK || r.asn.JobID != jobID {
			t.Fatalf("claim: %+v", r)
		}
		if elapsed > 1500*time.Millisecond {
			t.Fatalf("claim проспал %v — wake не сработал", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("claim не вернулся за 3 с")
	}
}

// 11. Фоновая задача: needs_attention через 10 мин истёкшего lease и таймаут
// этапа (часы двигаются вручную, без sleep).
func TestWorkerSweepTimeouts(t *testing.T) {
	h := newWorkerHarness(t)
	wtoken := h.seedWorker("pc-1")
	cookie := h.seedUserHTTP("c@local", "pw", store.RoleClient)
	jobID := h.submitJob(cookie, "Курсовой проект по базам данных с моделями")
	asn := h.claim(wtoken)

	// Lease истёк больше 10 минут назад → needs_attention, статус прежний.
	h.clock.Add(12 * time.Minute)
	if err := h.queue.Sweep(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	aj, err := h.st.JobByID(t.Context(), jobID)
	if err != nil || !aj.NeedsAttention || aj.Status != store.StatusRunning {
		t.Fatalf("job: %+v %v", aj, err)
	}
	if j := h.clientJob(cookie, jobID); j.ClientStatus != "in_progress" {
		t.Fatalf("клиенту ничего не меняется, got %q", j.ClientStatus)
	}

	// Таймаут этапа (3 ч от stage_started) → авто-повтор, старый epoch мёртв.
	h.clock.Add(3 * time.Hour)
	if err := h.queue.Sweep(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	aj, err = h.st.JobByID(t.Context(), jobID)
	if err != nil || aj.Status != store.StatusQueued || aj.Attempt != 1 {
		t.Fatalf("после таймаута: %+v %v", aj, err)
	}
	code, b := h.wreq(http.MethodPost, "/api/worker/jobs/"+jobID+"/heartbeat", wtoken, jsonReader(map[string]interface{}{"epoch": asn.Epoch}))
	if code != http.StatusConflict || !strings.Contains(string(b), "stale_lease") {
		t.Fatalf("старый epoch после таймаута: %d %s", code, b)
	}

	// Повторная выдача (attempt=1), снова таймаут → failed + needs_attention.
	asn2 := h.claim(wtoken)
	if asn2.Action != "start" || asn2.Attempt != 1 {
		t.Fatalf("assignment: %+v", asn2)
	}
	h.clock.Add(3*time.Hour + time.Minute)
	if err := h.queue.Sweep(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	aj, err = h.st.JobByID(t.Context(), jobID)
	if err != nil || aj.Status != store.StatusFailed || !aj.NeedsAttention ||
		aj.Error == nil || *aj.Error != "превышен таймаут этапа 3 ч" {
		t.Fatalf("после второго таймаута: %+v %v", aj, err)
	}
}
