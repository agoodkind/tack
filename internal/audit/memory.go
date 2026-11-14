package audit

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"goodkind.io/tack/internal/clock"
)

// MemoryRecorder captures events in memory for tests and integration
// harnesses. It supports concurrent use, but not production use.
type MemoryRecorder struct {
	mu     sync.Mutex
	events []Event
}

func NewMemoryRecorder() *MemoryRecorder { return &MemoryRecorder{} }

func (m *MemoryRecorder) Record(_ context.Context, ev Event) error {
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = clock.Now().UTC()
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

// Reset deletes all captured events between subtests.
func (m *MemoryRecorder) Reset() {
	m.mu.Lock()
	m.events = nil
	m.mu.Unlock()
}

// NoopRecorder discards every event. A deployment must set
// AUDIT_ALLOW_UNRECORDED to allow operation without recording. Do not use it
// when a recorder fails to open.
type NoopRecorder struct{}

func (NoopRecorder) Record(context.Context, Event) error { return nil }

// ErrRecorderUnwired reports a missing recorder rather than a backend failure.
var ErrRecorderUnwired = errors.New("audit: no recorder installed")

// UnwiredRecorder refuses events before startup installs a recorder.
// A missing recorder must fail rather than silently discard events.
type UnwiredRecorder struct{}

// Record refuses the event and reports the missing recorder.
func (UnwiredRecorder) Record(_ context.Context, ev Event) error {
	slog.Error(
		"audit.recorder_unwired",
		slog.String("verb", ev.Verb),
		slog.String("err", ErrRecorderUnwired.Error()),
	)

	return ErrRecorderUnwired
}
