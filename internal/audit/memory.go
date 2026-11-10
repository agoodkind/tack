package audit

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"goodkind.io/tack/internal/clock"
)

// MemoryRecorder stores events in memory for tests and integration harnesses.
// Its methods support concurrent calls. Production must use a durable recorder.
type MemoryRecorder struct {
	mu     sync.Mutex
	events []Event
}

func NewMemoryRecorder() *MemoryRecorder { return &MemoryRecorder{} }

func (m *MemoryRecorder) Record(_ context.Context, event Event) error {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = clock.Now().UTC()
	}

	m.mu.Lock()
	m.events = append(m.events, event)
	m.mu.Unlock()

	return nil
}

// Events returns a copy of every event that MemoryRecorder has recorded.
//
// The method copies the event slice while the recorder's mutex protects it.
// The copy does not deep-copy nested values inside each event.
//
//   - Changes to the returned slice do not replace stored slice entries.
//   - Nested event values retain their existing references.
func (m *MemoryRecorder) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, len(m.events))
	copy(out, m.events)

	return out
}

// Reset removes every event from MemoryRecorder.
//
// Tests can call Reset between subtests instead of creating another recorder.
// The method locks the same mutex that protects recording and event reads.
//
//  1. Call Reset after the preceding subtest finishes using its events.
//  2. Record the events for the next subtest.
func (m *MemoryRecorder) Reset() {
	m.mu.Lock()
	m.events = nil
	m.mu.Unlock()
}

// NoopRecorder discards every event. Runtime selection requires
// AUDIT_ALLOW_UNRECORDED when a deployment lacks an audit backend. The flag
// expresses the operator's intent to run without recording. Do not substitute
// NoopRecorder after a recorder fails to open.
//
// The audit completeness contract requires durable recording before user-data
// operations. Selecting NoopRecorder does not meet that requirement.
type NoopRecorder struct{}

func (NoopRecorder) Record(context.Context, Event) error { return nil }

// ErrRecorderUnwired reports that startup did not install a recorder. The
// error distinguishes missing recorder setup from an audit backend failure.
var ErrRecorderUnwired = errors.New("audit: no recorder installed")

// UnwiredRecorder rejects events until startup installs a recorder. The
// earlier default silently discarded events, and missing recorder setup could
// leave a server apparently healthy without recording its actions.
type UnwiredRecorder struct{}

// Record rejects the event with [ErrRecorderUnwired].
//
// The method logs the event verb and the missing recorder error. It does not
// discard the event while reporting success.
//
//   - The return value always equals ErrRecorderUnwired.
//   - The log event uses audit.recorder_unwired.
func (UnwiredRecorder) Record(_ context.Context, ev Event) error {
	slog.Error(
		"audit.recorder_unwired",
		slog.String("verb", ev.Verb),
		slog.String("err", ErrRecorderUnwired.Error()),
	)

	return ErrRecorderUnwired
}
