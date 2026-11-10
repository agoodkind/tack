package audit

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

type closingRecorder struct {
	closed bool
}

func (r *closingRecorder) Record(context.Context, Event) error { return nil }

func (r *closingRecorder) Close() error {
	r.closed = true
	return nil
}

type openRecorder struct{}

func (openRecorder) Record(context.Context, Event) error { return nil }

type plainClosingRecorder struct {
	closed bool
}

func (r *plainClosingRecorder) Record(context.Context, Event) error { return nil }

func (r *plainClosingRecorder) Close() { r.closed = true }

type captureRecorder struct {
	events []Event
}

func (r *captureRecorder) Record(_ context.Context, event Event) error {
	r.events = append(r.events, event)
	return nil
}

func TestCanonicalRecorderRecordsEveryEvent(t *testing.T) {
	inner := &captureRecorder{}
	recorder := CanonicalRecorder{Inner: inner}

	if err := recorder.Record(context.Background(), Event{Verb: "test.event"}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if len(inner.events) != 1 {
		t.Fatalf("recorded events = %d, want 1", len(inner.events))
	}
	event := inner.events[0]
	if event.EventID == uuid.Nil {
		t.Fatal("EventID is nil")
	}
	if event.OccurredAt.IsZero() || event.OccurredAt.After(time.Now().UTC()) {
		t.Fatalf("OccurredAt = %v, want current timestamp", event.OccurredAt)
	}
}

func TestCanonicalRecorderCloseClosesInner(t *testing.T) {
	inner := &closingRecorder{}
	recorder := CanonicalRecorder{Inner: inner}

	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !inner.closed {
		t.Fatal("inner Close() was not called")
	}
}

func TestCanonicalRecorderCloseWithoutInnerCloser(t *testing.T) {
	recorder := CanonicalRecorder{Inner: openRecorder{}}

	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestCanonicalRecorderCloseClosesInnerWithoutError(t *testing.T) {
	inner := &plainClosingRecorder{}
	recorder := CanonicalRecorder{Inner: inner}

	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !inner.closed {
		t.Fatal("inner Close() was not called")
	}
}
