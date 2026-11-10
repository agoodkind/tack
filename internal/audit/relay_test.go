package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
)

// TestRelayYugabyteProducedEventIsDeleted checks that the relay deletes an
// outbox row only after the recorder accepts its event.
func TestRelayYugabyteProducedEventIsDeleted(t *testing.T) {
	event := relayTestEvent("ops.test.yugabyte")
	outbox := &relayYugabyteFake{rows: []OutboxRow{{EventID: event.EventID, Event: event}}}

	recorder, relay := newRelayTest(t, outbox, nil, -1)
	relay.Start(t.Context())
	waitForRelay(t, func() bool { return outbox.deletedCount() == 1 })

	if got := len(recorder.inner.Events()); got != 1 {
		t.Fatalf("recorded events = %d, want 1", got)
	}

	if got := outbox.rowCount(); got != 0 {
		t.Fatalf("Yugabyte outbox rows = %d, want 0", got)
	}
}

// TestRelayYugabyteProduceFailureLeavesRow checks that a refused event stays
// in the outbox for retry on the next poll. Audit events must not disappear
// after a publication failure. Repeated delivery is safe because the consumer
// deduplicates event IDs.
func TestRelayYugabyteProduceFailureLeavesRow(t *testing.T) {
	event := relayTestEvent("ops.test.yugabyte.failure")
	outbox := &relayYugabyteFake{rows: []OutboxRow{{EventID: event.EventID, Event: event}}}

	_, relay := newRelayTest(t, outbox, nil, 0)
	relay.Start(t.Context())
	waitForRelay(t, func() bool { return outbox.readCount() > 0 })

	if got := outbox.deletedCount(); got != 0 {
		t.Fatalf("deleted rows after produce failure = %d, want 0", got)
	}

	if got := outbox.rowCount(); got != 1 {
		t.Fatalf("Yugabyte outbox rows = %d, want 1", got)
	}
}

// TestRelayFoundationDBClearsThroughLastAcceptedMark checks a publication
// failure within a batch. The relay clears entries only through the last
// accepted mark and retains the refused entry and later entries for retry.
func TestRelayFoundationDBClearsThroughLastAcceptedMark(t *testing.T) {
	firstJSON := relayTestEventJSON(t, "ops.test.foundationdb.first")
	secondJSON := relayTestEventJSON(t, "ops.test.foundationdb.second")
	foundationDB := &relayFoundationDBFake{
		entries: []RelayOutboxEntry{
			{Mark: []byte("mark-1"), Event: firstJSON},
			{Mark: []byte("mark-2"), Event: secondJSON},
		},
	}

	recorder, relay := newRelayTest(t, &relayYugabyteFake{}, foundationDB, 1)
	relay.Start(t.Context())
	waitForRelay(t, func() bool { return len(foundationDB.clearedMarks()) == 1 })

	cleared := foundationDB.clearedMarks()
	if !bytes.Equal(cleared[0], []byte("mark-1")) {
		t.Fatalf("cleared mark = %q, want %q", cleared[0], "mark-1")
	}

	if got := foundationDB.rowCount(); got != 1 {
		t.Fatalf("FoundationDB outbox rows = %d, want 1", got)
	}

	if got := len(recorder.inner.Events()); got != 1 {
		t.Fatalf("recorded events = %d, want 1", got)
	}
}

// relayTestRecorder uses an in-memory recorder until the call index equals
// failAt, then refuses that call and later calls. A negative failAt disables
// refusals.
type relayTestRecorder struct {
	inner  *MemoryRecorder
	failAt int
	mu     sync.Mutex
	calls  int
}

func (r *relayTestRecorder) Record(ctx context.Context, event Event) error {
	r.mu.Lock()
	call := r.calls
	r.calls++
	r.mu.Unlock()
	if r.failAt >= 0 && call >= r.failAt {
		return errors.New("test produce failure")
	}

	return r.inner.Record(ctx, event)
}

type relayYugabyteFake struct {
	mu      sync.Mutex
	rows    []OutboxRow
	deletes []uuid.UUID
	reads   int
}

func (f *relayYugabyteFake) ReadBatch(_ context.Context, _ int) ([]OutboxRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	rows := make([]OutboxRow, len(f.rows))
	copy(rows, f.rows)

	return rows, nil
}

func (f *relayYugabyteFake) Delete(_ context.Context, eventID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for i, row := range f.rows {
		if row.EventID == eventID {
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			f.deletes = append(f.deletes, eventID)

			return nil
		}
	}

	return nil
}

func (f *relayYugabyteFake) deletedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.deletes)
}

func (f *relayYugabyteFake) rowCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.rows)
}

func (f *relayYugabyteFake) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.reads
}

type relayFoundationDBFake struct {
	mu      sync.Mutex
	entries []RelayOutboxEntry
	cleared [][]byte
}

func (f *relayFoundationDBFake) ReadOutboxFrom(
	_ context.Context,
	mark []byte,
	limit int,
) ([]RelayOutboxEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	start := 0
	if len(mark) > 0 {
		for i, entry := range f.entries {
			if bytes.Equal(entry.Mark, mark) {
				start = i + 1
				break
			}
		}
	}

	if start >= len(f.entries) {
		return nil, nil
	}

	end := start + limit
	if end > len(f.entries) {
		end = len(f.entries)
	}

	entries := make([]RelayOutboxEntry, end-start)
	copy(entries, f.entries[start:end])

	return entries, nil
}

func (f *relayFoundationDBFake) ClearThrough(_ context.Context, mark []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for i, entry := range f.entries {
		if bytes.Equal(entry.Mark, mark) {
			f.cleared = append(f.cleared, append([]byte(nil), mark...))
			f.entries = f.entries[i+1:]

			return nil
		}
	}

	return nil
}

func (f *relayFoundationDBFake) clearedMarks() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()

	marks := make([][]byte, len(f.cleared))
	for i, mark := range f.cleared {
		marks[i] = append([]byte(nil), mark...)
	}

	return marks
}

func (f *relayFoundationDBFake) rowCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.entries)
}

func newRelayTest(
	t *testing.T,
	yugabyte YugabyteOutbox,
	foundationDB FoundationDBOutbox,
	failAt int,
) (*relayTestRecorder, *Relay) {
	t.Helper()
	recorder := &relayTestRecorder{
		inner:  NewMemoryRecorder(),
		failAt: failAt,
		mu:     sync.Mutex{},
		calls:  0,
	}
	relay, err := NewRelay(RelayConfig{
		Recorder:     recorder,
		Yugabyte:     yugabyte,
		FoundationDB: foundationDB,
		PollInterval: 2 * time.Millisecond,
		BatchSize:    10,
	})
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}

	t.Cleanup(func() {
		if closeErr := relay.Close(); closeErr != nil {
			t.Errorf("close relay: %v", closeErr)
		}
	})

	return recorder, relay
}

func relayTestEvent(verb string) Event {
	return Event{
		Verb:       verb,
		EventID:    uuid.Must(uuid.NewV7()),
		OccurredAt: clock.Now().UTC(),
	}
}

func relayTestEventJSON(t *testing.T, verb string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(relayTestEvent(verb))
	if err != nil {
		t.Fatalf("marshal %s: %v", verb, err)
	}

	return raw
}

// TestRelayCloseReturnsWhenADrainIsStuck checks the shutdown wait limit. Close
// cannot cancel an active database call and must return before that call
// finishes instead of blocking audit-consumer shutdown.
func TestRelayCloseReturnsWhenADrainIsStuck(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stuck := &relayStuckOutbox{release: release}

	relay, err := NewRelay(RelayConfig{
		Recorder: &relayTestRecorder{
			inner:  NewMemoryRecorder(),
			failAt: -1,
			mu:     sync.Mutex{},
			calls:  0,
		},
		Yugabyte:     stuck,
		FoundationDB: nil,
		PollInterval: time.Millisecond,
		BatchSize:    1,
	})
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}

	relay.Start(t.Context())
	waitForRelay(t, stuck.entered)

	done := make(chan error, 1)
	go func() { done <- relay.Close() }()
	select {
	case closeErr := <-done:
		if closeErr != nil {
			t.Fatalf("close relay: %v", closeErr)
		}
	case <-time.After(relayDrainGrace + 5*time.Second):
		t.Fatal("Close did not return while a drain was stuck")
	}
}

// relayStuckOutbox blocks ReadBatch until the test releases it. The fixture
// represents a database that does not answer.
type relayStuckOutbox struct {
	release chan struct{}
	mu      sync.Mutex
	inside  bool
}

func (o *relayStuckOutbox) ReadBatch(_ context.Context, _ int) ([]OutboxRow, error) {
	o.mu.Lock()
	o.inside = true
	o.mu.Unlock()
	<-o.release

	return nil, nil
}

func (o *relayStuckOutbox) Delete(_ context.Context, _ uuid.UUID) error { return nil }

func (o *relayStuckOutbox) entered() bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.inside
}

func waitForRelay(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()

	for {
		if condition() {
			return
		}

		select {
		case <-deadline.C:
			t.Fatal("relay condition was not reached")
		case <-ticker.C:
		}
	}
}
