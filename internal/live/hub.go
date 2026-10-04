// Package live is an in-memory SSE hub. Each job has a set of subscribers;
// publishing an event fans it out to all of them.
package live

import (
	"sync"
)

// Event is a named SSE event with a JSON payload.
type Event struct {
	Name string
	Data []byte
}

// Subscription is a single consumer of a job's events.
type Subscription struct {
	C    chan Event
	job  string
	hub  *Hub
	once sync.Once
}

// Close unsubscribes. Safe to call multiple times.
func (s *Subscription) Close() {
	s.once.Do(func() {
		s.hub.remove(s.job, s)
		close(s.C)
	})
}

// Hub fans out events per job id.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[*Subscription]struct{}
}

// New creates an empty hub.
func New() *Hub {
	return &Hub{subs: make(map[string]map[*Subscription]struct{})}
}

// Subscribe registers a subscriber for a job. buffer is the channel capacity.
func (h *Hub) Subscribe(jobID string, buffer int) *Subscription {
	s := &Subscription{C: make(chan Event, buffer), job: jobID, hub: h}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[jobID] == nil {
		h.subs[jobID] = make(map[*Subscription]struct{})
	}
	h.subs[jobID][s] = struct{}{}
	return s
}

// Publish sends an event to all subscribers of a job. Slow subscribers are
// skipped rather than blocking the publisher.
func (h *Hub) Publish(jobID string, ev Event) {
	h.mu.Lock()
	subs := make([]*Subscription, 0, len(h.subs[jobID]))
	for s := range h.subs[jobID] {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	for _, s := range subs {
		select {
		case s.C <- ev:
		default:
		}
	}
}

func (h *Hub) remove(jobID string, s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set, ok := h.subs[jobID]; ok {
		delete(set, s)
		if len(set) == 0 {
			delete(h.subs, jobID)
		}
	}
}
