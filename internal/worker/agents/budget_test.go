package agents

import (
	"fmt"
	"strings"
	"testing"
)

// Пункт 3 (третий раунд): пустой шаг тоже расходует бюджет, а оценка памяти
// не подменяется длиной JSON — учитываются фиксированная стоимость шага и
// накладные расходы контейнеров payload.
func TestStepSizeIsConservative(t *testing.T) {
	if got := stepSize(NewStep{}); got != stepOverheadBytes {
		t.Fatalf("empty step size = %d, want %d", got, stepOverheadBytes)
	}
	if got := stepSize(NewStep{Type: "message"}); got != stepOverheadBytes {
		t.Fatalf("step without payload/summary = %d, want %d", got, stepOverheadBytes)
	}

	payload := map[string]interface{}{
		"command":   "ls",
		"output":    strings.Repeat("x", 4096),
		"changes":   []map[string]interface{}{{"path": "out/a.txt", "kind": "add"}},
		"exit_code": 0,
		"nested":    map[string]interface{}{"a": "b", "c": []interface{}{"d", 1}},
	}
	got := stepSize(NewStep{Summary: "s", Payload: payload})
	if got < 4096+stepOverheadBytes+5*payloadEntryBytes {
		t.Fatalf("step size %d ignores the 4 KiB string or the container overhead", got)
	}

	// Many tiny entries cost far more than their raw bytes: the per-entry
	// overhead is charged, so JSON length cannot substitute the estimate.
	many := map[string]interface{}{}
	for i := 0; i < 100; i++ {
		many[fmt.Sprintf("k%02d", i)] = "v"
	}
	perEntry := stepSize(NewStep{Payload: many}) / 100
	if perEntry <= len("k00")+len("v") {
		t.Fatalf("per-entry cost %d ignores container overhead", perEntry)
	}
	if payloadSize([]interface{}{"a", "b"}) <= 2 {
		t.Fatal("slice elements are not charged")
	}
}

// Пункт 3 (третий раунд): конечный предел числа шагов. Миллион пустых шагов
// не создаёт неограниченный срез: очередь упирается в лимит и отбрасывает
// лишнее, а пачка в полёте тоже считается.
func TestStepQueueShortStepsBounded(t *testing.T) {
	oldBytes, oldSteps := maxQueueBytes, maxQueueSteps
	maxQueueBytes = 64 * 1024
	maxQueueSteps = 64
	t.Cleanup(func() { maxQueueBytes, maxQueueSteps = oldBytes, oldSteps })

	q := newStepQueue()
	q.push(NewStep{Seq: 1, Type: "message"})
	if q.bytes != stepOverheadBytes {
		t.Fatalf("empty step cost = %d, want %d", q.bytes, stepOverheadBytes)
	}
	for i := 2; i <= 100000; i++ {
		q.push(NewStep{Seq: i, Type: "message"})
	}
	batch, sizes, closed := q.take()
	if closed {
		t.Fatal("queue closed")
	}
	if len(batch) != maxQueueSteps {
		t.Fatalf("queued steps = %d, want the step limit %d", len(batch), maxQueueSteps)
	}
	if q.inFlight != maxQueueSteps {
		t.Fatalf("in-flight = %d, want %d", q.inFlight, maxQueueSteps)
	}
	// While the batch is in flight the step limit covers it.
	q.push(NewStep{Seq: 200000, Type: "message"})
	if more, _, _ := q.take(); len(more) != 0 {
		t.Fatalf("in-flight steps are not counted: %d more queued", len(more))
	}
	q.release(sizes)
	q.push(NewStep{Seq: 200001, Type: "message"})
	more, _, _ := q.take()
	if len(more) != 1 || more[0].Seq != 200001 {
		t.Fatalf("after release take = %+v", more)
	}
}

// Пункт 3 (третий раунд): смесь коротких и больших payload при
// заблокированной отправке — бюджет общий для очереди и пачки в полёте.
func TestStepBudgetMixedPayloadInFlight(t *testing.T) {
	oldBytes, oldSteps := maxQueueBytes, maxQueueSteps
	maxQueueBytes = 4 * 1024
	maxQueueSteps = 1000
	t.Cleanup(func() { maxQueueBytes, maxQueueSteps = oldBytes, oldSteps })

	q := newStepQueue()
	big := NewStep{Seq: 1, Type: "command", Payload: map[string]interface{}{"output": strings.Repeat("x", 1024)}}
	small := func(seq int) NewStep { return NewStep{Seq: seq, Type: "message"} }
	q.push(big)
	for i := 2; i <= 5; i++ {
		q.push(small(i))
	}
	batch, sizes, _ := q.take()
	if len(batch) != 5 {
		t.Fatalf("queued = %d, want 5", len(batch))
	}
	wantBig := stepOverheadBytes + payloadEntryBytes + len("output") + payloadEntryBytes + 1024 + payloadEntryBytes
	if sizes[0] != wantBig {
		t.Fatalf("big step size = %d, want %d", sizes[0], wantBig)
	}
	// In flight: 1478 + 4*256 = 2502 bytes of 4096.
	q.push(small(6)) // 2758 fits
	q.push(big)      // 4236 > cap: dropped
	more, _, _ := q.take()
	if len(more) != 1 || more[0].Seq != 6 {
		t.Fatalf("in-flight budget not enforced: %+v", more)
	}
}
