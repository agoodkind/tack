package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// failOnceRecorder fails its first N Record calls then succeeds. Used to
// exercise the drainer's retry path: the segment file must remain in place
// across failures and only get deleted after a successful pass.
type failOnceRecorder struct {
	mu       sync.Mutex
	fails    int
	captured []Event
}

func (f *failOnceRecorder) Record(_ context.Context, ev Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails > 0 {
		f.fails--
		return errors.New("transient")
	}
	f.captured = append(f.captured, ev)
	return nil
}

func TestWALAppendFsyncsAndDrains(t *testing.T) {
	dir := t.TempDir()
	inner := &failOnceRecorder{}
	w, err := NewWALRecorder(t.Context(), inner, WALConfig{
		Dir:           dir,
		DrainInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer func() { _ = w.Close() }()

	requestID := "req-wal"
	traceID := "trace-wal"
	for i := 0; i < 25; i++ {
		err := w.Record(t.Context(), Event{
			Verb:   string(VerbNodeRead),
			Actor:  Actor{Type: ActorUser, ID: uuid.Must(uuid.NewV7())},
			Entity: Entity{Type: "node", ID: uuid.Must(uuid.NewV7())},
			Context: EventContext{
				OrgID:     uuid.Must(uuid.NewV7()),
				Source:    SourceMCP,
				RequestID: requestID,
				TraceID:   traceID,
			},
		})
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	// Close forces a final drain even when the active segment has not
	// rotated, which is the documented contract: every successfully
	// appended event reaches the inner Recorder by the time Close returns.
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	inner.mu.Lock()
	got := len(inner.captured)
	if got != 25 {
		inner.mu.Unlock()
		t.Fatalf("inner saw %d events, want 25", got)
	}
	first := inner.captured[0]
	inner.mu.Unlock()
	if first.Context.RequestID != requestID || first.Context.TraceID != traceID {
		t.Fatalf("correlation lost during replay: %#v", first.Context)
	}
}

func TestWALStateChangeBypassesQueue(t *testing.T) {
	dir := t.TempDir()
	inner := &failOnceRecorder{}
	w, err := NewWALRecorder(t.Context(), inner, WALConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	err = w.Record(t.Context(), Event{
		Verb:    string(VerbNodeCreate),
		Actor:   Actor{Type: ActorUser, ID: uuid.Must(uuid.NewV7())},
		Entity:  Entity{Type: "node", ID: uuid.Must(uuid.NewV7())},
		Context: EventContext{OrgID: uuid.Must(uuid.NewV7())},
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	inner.mu.Lock()
	defer inner.mu.Unlock()
	if len(inner.captured) != 1 || inner.captured[0].Verb != string(VerbNodeCreate) {
		t.Fatalf("state-change verb did not bypass WAL: %#v", inner.captured)
	}
	// No segment file should have been written, since the verb skipped the WAL.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".wal" {
			t.Errorf("unexpected segment file %s", e.Name())
		}
	}
}

func TestWALOverflowDropsAndCounts(t *testing.T) {
	dir := t.TempDir()
	// Drop a stale segment in place to simulate a stuck drainer.
	stale := filepath.Join(dir, time.Now().UTC().Add(-2*time.Hour).Format("20060102T150405")+".wal")
	if err := os.WriteFile(stale, []byte{0}, 0o600); err != nil {
		t.Fatal(err)
	}
	inner := &failOnceRecorder{}
	w, err := NewWALRecorder(t.Context(), inner, WALConfig{
		Dir:           dir,
		MaxLag:        time.Hour,
		DrainInterval: time.Hour, // disable drainer for the duration of the test
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	err = w.Record(t.Context(), Event{
		Verb:    string(VerbNodeRead),
		Actor:   Actor{Type: ActorUser, ID: uuid.Must(uuid.NewV7())},
		Entity:  Entity{Type: "node", ID: uuid.Must(uuid.NewV7())},
		Context: EventContext{OrgID: uuid.Must(uuid.NewV7())},
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if got := w.dropped.Load(); got != 1 {
		t.Errorf("dropped counter = %d, want 1", got)
	}
}
