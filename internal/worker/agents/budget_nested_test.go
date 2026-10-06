package agents

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// nestedChangesLine builds one codex item.completed/file_change event whose
// changes array carries an extra array of n nulls. With n = 100000 the line is
// 500098 bytes: below parserLineLimit, so the parser sees it unchanged.
func nestedChangesLine(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"type":"item.completed","item":{"id":"c","type":"file_change","changes":[{"path":"out/a.txt","extra":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("null")
	}
	b.WriteString(`]}]}}`)
	return []byte(b.String())
}

// Пункт 1 (4-й раунд): стоимость контейнера и каждого слота считается
// отдельно от стоимости значения, включая null, а ёмкость backing array —
// как занятая, так и незаполненные слоты.
func TestPayloadSizeChargesSlotsAndCapacity(t *testing.T) {
	// A null element still occupies its slot: it cannot be free.
	if got := payloadSize([]interface{}{nil}); got < payloadSlotBytes {
		t.Fatalf("one null slot = %d, want >= %d", got, payloadSlotBytes)
	}
	nulls := []interface{}{nil, nil, nil}
	if got := payloadSize(nulls); got < 3*payloadSlotBytes {
		t.Fatalf("three null slots = %d, want >= %d", got, 3*payloadSlotBytes)
	}
	// Capacity beyond length (unfilled slots) is charged too.
	sparse := make([]interface{}, 2, 1000)
	if got := payloadSize(sparse); got < 1000*payloadSlotBytes {
		t.Fatalf("capacity 1000 = %d, want >= %d", got, 1000*payloadSlotBytes)
	}
	// A nil map inside a []map is a slot as well.
	maps := []map[string]interface{}{nil, nil}
	if got := payloadSize(maps); got < 2*payloadSlotBytes {
		t.Fatalf("nil map slots = %d, want >= %d", got, 2*payloadSlotBytes)
	}
	// Nested empty containers keep their header cost.
	empty := map[string]interface{}{"a": []interface{}{}, "b": map[string]interface{}{}}
	if got := payloadSize(empty); got <= payloadEntryBytes {
		t.Fatalf("empty nested containers = %d, want > %d", got, payloadEntryBytes)
	}
	// The real codex shape: changes -> extra with nulls.
	payload := map[string]interface{}{
		"changes": []map[string]interface{}{{"path": "out/a.txt", "extra": []interface{}{nil, nil, nil, nil}}},
	}
	if got := payloadSize(payload); got < 4*payloadSlotBytes {
		t.Fatalf("changes with nulls = %d, want >= %d", got, 4*payloadSlotBytes)
	}
	// String elements keep their value cost on top of their slots.
	strs := []interface{}{"aaaa", "bbbb"}
	if got := payloadSize(strs); got < 2*payloadSlotBytes+2*(4+payloadEntryBytes) {
		t.Fatalf("string elements = %d, want slots plus values", got)
	}
	// Other JSON containers a parser may keep are charged as well.
	if got := payloadSize([]float64{1, 2, 3}); got < 3*payloadSlotBytes {
		t.Fatalf("typed slice = %d, want >= %d", got, 3*payloadSlotBytes)
	}
	if got := payloadSize(map[string]string{"a": "b"}); got <= payloadEntryBytes {
		t.Fatalf("typed map = %d, want > %d", got, payloadEntryBytes)
	}
}

// Пункт 1 (4-й раунд): воспроизведение из отчёта. Действующий CodexParser
// разбирает 60 событий file_change с массивом из 100000 null в каждом; первый
// шаг уходит в пачку в полёте, отправка удержана. Очередь упирается в общий
// лимит и отбрасывает лишнее, а удержанный heap после GC не превышает бюджет
// с разумным допуском.
func TestParserNestedArraySlotsBounded(t *testing.T) {
	const (
		events = 60
		nulls  = 100000
	)
	line := nestedChangesLine(nulls)
	if len(line) >= parserLineLimit {
		t.Fatalf("line %d bytes, want below the truncation threshold %d", len(line), parserLineLimit)
	}
	if len(line) < 500_000 {
		t.Fatalf("line = %d bytes, want the ~500 KB reproduction from the report", len(line))
	}
	t.Logf("reproduction line = %d bytes", len(line))

	p := NewCodexParser()
	q := newStepQueue()
	seq := 0
	drain := func() {
		for _, s := range p.Drain() {
			seq++
			q.push(NewStep{Seq: seq, Ts: s.Ts, Type: s.Type, Summary: s.Summary, Payload: s.Payload})
		}
	}

	// A tiny event first, so one-off parser/runtime allocations do not skew
	// the heap delta.
	p.Feed([]byte(`{"type":"item.completed","item":{"id":"w","type":"agent_message","text":"x"}}`), time.Now())
	drain()
	q.take()
	before := heapAlloc()

	// The first big step is taken by the sender and stays in flight.
	p.Feed(line, time.Now())
	drain()
	inFlight, inFlightSizes, _ := q.take()
	if len(inFlight) != 1 {
		t.Fatalf("in-flight steps = %d, want 1", len(inFlight))
	}
	inFlightBytes := 0
	for _, size := range inFlightSizes {
		inFlightBytes += size
	}

	// The rest of the stream queues up behind it.
	for i := 1; i < events; i++ {
		p.Feed(line, time.Now())
		drain()
	}

	// q.bytes covers the queued steps and the in-flight batch together (the
	// batch's bytes are released only when Send returns).
	accounted := q.bytes
	during := heapAlloc()
	queued, _, _ := q.take()
	accepted := len(inFlight) + len(queued)
	t.Logf("accounted=%d (in-flight %d) accepted=%d heap_delta=%d",
		accounted, inFlightBytes, accepted, int64(during)-int64(before))

	if accepted >= events {
		t.Fatalf("accepted steps = %d, want the finite limit to drop some of %d", accepted, events)
	}
	if accounted > maxQueueBytes {
		t.Fatalf("accounted bytes = %d, want <= %d", accounted, maxQueueBytes)
	}
	if delta := int64(during) - int64(before); delta > int64(maxQueueBytes)*3/2 {
		t.Fatalf("heap grew by %d bytes for %d nested events, want <= %d (1.5x the budget)",
			delta, events, int64(maxQueueBytes)*3/2)
	}
	// The accepted steps are held; the dropped ones stay only in the raw log.
	if accepted == 0 {
		t.Fatal("no steps accepted at all")
	}
}

// Пункт 1 (4-й раунд): то же воспроизведение через настоящий Run. Первая
// пачка уходит в полёт и Send удержан, поэтому очередь упирается в лимит;
// лишние события остаются только в полном сыром логе, а после восстановления
// отправки удержанные структуры освобождаются.
func TestRunNestedArraySlotsReleased(t *testing.T) {
	fastFlush(t, 50*time.Millisecond, 1)
	const (
		events = 60
		nulls  = 100000
	)
	marker := filepath.Join(t.TempDir(), "produced")
	spec := helperSpec(t,
		"HELPER_MODE=changes",
		"HELPER_LINES="+strconv.Itoa(events),
		"HELPER_NULLS="+strconv.Itoa(nulls),
		"HELPER_TAIL_LINES=2",
		"HELPER_MARKER="+marker,
		"HELPER_EXIT=0",
	)

	before := heapAlloc()
	var during uint64
	var first sync.Once
	var mu sync.Mutex
	got := 0
	spec.Send = func(steps []NewStep) error {
		blocked := false
		first.Do(func() { blocked = true })
		if blocked {
			// Nothing is sent while the helper produces the stream, so the
			// queue reaches its finite limit.
			deadline := time.Now().Add(180 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Error("helper did not finish producing events")
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			during = heapAlloc()
		}
		mu.Lock()
		got += len(steps)
		mu.Unlock()
		return nil
	}

	res, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}

	// The raw log keeps every event, including the tail.
	info, err := os.Stat(spec.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < int64(events*500_000) {
		t.Fatalf("raw log = %d bytes, want the full %d-event stream", info.Size(), events)
	}
	if tail := lastBytes(t, spec.LogPath, 4096); !strings.Contains(tail, "after changes 2") {
		t.Fatalf("raw log is missing the last event: %q", tail)
	}

	// Only the accepted subset was sent; the excess stayed in the raw log.
	if got == 0 || got >= events {
		t.Fatalf("sent steps = %d, want a bounded subset of %d", got, events)
	}
	// While the send was held, the retained heap stayed inside the budget
	// with a runtime allowance.
	if delta := int64(during) - int64(before); delta > int64(maxQueueBytes)*3/2 {
		t.Fatalf("heap grew by %d bytes for %d nested events, want <= %d", delta, events, int64(maxQueueBytes)*3/2)
	}
	// After the send recovered the held structures are released again.
	if delta := int64(heapAlloc()) - int64(before); delta > 32<<20 {
		t.Fatalf("heap still holds %d bytes after the run, want < 32 MiB", delta)
	}
}
