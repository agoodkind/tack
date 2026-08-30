package audit

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"goodkind.io/tack/internal/clock"
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

// Reset drops all captured events; useful between subtests.
func (m *MemoryRecorder) Reset() {
	m.mu.Lock()
	m.events = nil
	m.mu.Unlock()
}

// NoopRecorder discards every event. A deployment reaches it only by setting
// AUDIT_ALLOW_UNRECORDED, which is the operator saying unrecorded operation is
// intended. Nothing may substitute it for a recorder that failed to open.
type NoopRecorder struct{}

func (NoopRecorder) Record(context.Context, Event) error { return nil }

// ErrRecorderUnwired is what an uninstalled sink returns. It names a wiring
// bug rather than a backend failure, so a reader of the log can tell the two
// apart.
var ErrRecorderUnwired = errors.New("audit: no recorder installed")

// UnwiredRecorder is the sink a boundary holds before startup installs the
// real one. It refuses and says so, because the previous default discarded
// events silently: a wiring mistake then produced a server that recorded
// nothing and looked healthy from outside.
type UnwiredRecorder struct{}

// Record refuses the event and names the wiring bug that caused it.
func (UnwiredRecorder) Record(_ context.Context, ev Event) error {
	slog.Error("audit.recorder_unwired",
		slog.String("verb", ev.Verb),
		slog.String("err", ErrRecorderUnwired.Error()),
	)
	return ErrRecorderUnwired
}
