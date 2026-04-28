package audit

import (
	"context"
	"sync"
	"time"
)

// MemoryRecorder captures events in memory for tests and integration
// harnesses. Safe for concurrent use. Not for production.
type MemoryRecorder struct {
	mu     sync.Mutex
	events []Event
}

func NewMemoryRecorder() *MemoryRecorder { return &MemoryRecorder{} }

func (m *MemoryRecorder) Record(_ context.Context, ev Event) error {
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	m.mu.Lock()
	m.events = append(m.events, ev)
	m.mu.Unlock()
	return nil
}

// Events returns a copy of every event captured so far.
func (m *MemoryRecorder) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, len(m.events))
	copy(out, m.events)
	return out
}

// Reset drops all captured events; useful between subtests.
func (m *MemoryRecorder) Reset() {
	m.mu.Lock()
	m.events = nil
	m.mu.Unlock()
}

// NoopRecorder discards every event. Useful when wiring needs a non-nil
// Recorder before the production implementation is ready.
type NoopRecorder struct{}

func (NoopRecorder) Record(context.Context, Event) error { return nil }
