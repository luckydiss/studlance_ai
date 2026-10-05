package live

import (
	"testing"
	"time"
)

func TestHubPublishSubscribe(t *testing.T) {
	h := New()
	sub := h.Subscribe("job1", 4)
	defer sub.Close()

	h.Publish("job1", Event{Name: "job", Data: []byte("{}")})
	select {
	case ev := <-sub.C:
		if ev.Name != "job" {
			t.Fatalf("event name %q", ev.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("no event received")
	}

	// Events for another job are not delivered.
	h.Publish("job2", Event{Name: "job"})
	select {
	case <-sub.C:
		t.Fatal("unexpected event for another job")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubCloseRemoves(t *testing.T) {
	h := New()
	sub := h.Subscribe("job1", 1)
	sub.Close()
	sub.Close() // idempotent
	h.Publish("job1", Event{Name: "job"})
	_, ok := <-sub.C
	if ok {
		t.Fatal("channel should be closed")
	}
}
