package audit

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// failingRecorder returns a fixed error from every Record call. When
// Sentinel is non-nil it is returned verbatim so tests can assert via
// errors.Is.
type failingRecorder struct {
	Sentinel error
	calls    int
	last     Event
}

func (f *failingRecorder) Record(_ context.Context, ev Event) error {
	f.calls++
	f.last = ev
	return f.Sentinel
}

// observingRecorder records the context in which Record was called so a
// test can assert that ctx cancellation was visible to the underlying
// recorder.
type observingRecorder struct {
	calls   int
	ctxErrs []error
	events  []Event
}

func (o *observingRecorder) Record(ctx context.Context, ev Event) error {
	o.calls++
	o.events = append(o.events, ev)
	o.ctxErrs = append(o.ctxErrs, ctx.Err())
	return ctx.Err()
}

func sampleEvent(t *testing.T) Event {
	t.Helper()
	return Event{
		Verb: "node.read",
		Actor: Actor{
			Type: ActorUser,
			ID:   uuid.New(),
		},
		Entity: Entity{
			Type: "node",
			ID:   uuid.New(),
		},
		Context: EventContext{
			OrgID:  uuid.New(),
			Source: SourceMCP,
		},
		Outcome: OutcomeOK,
	}
}

func TestDualWritesToBoth(t *testing.T) {
	primary := NewMemoryRecorder()
	secondary := NewMemoryRecorder()
	dual, err := NewDualRecorder(primary, secondary)
	if err != nil {
		t.Fatalf("NewDualRecorder: %v", err)
	}
	ev := sampleEvent(t)
	if err := dual.Record(context.Background(), ev); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got := len(primary.Events()); got != 1 {
		t.Fatalf("primary recorder: want 1 event, got %d", got)
	}
	if got := len(secondary.Events()); got != 1 {
		t.Fatalf("secondary recorder: want 1 event, got %d", got)
	}
}

func TestDualReturnsFirstError(t *testing.T) {
	sentinel := errors.New("primary boom")
	primary := &failingRecorder{Sentinel: sentinel}
	secondary := NewMemoryRecorder()
	dual, err := NewDualRecorder(primary, secondary)
	if err != nil {
		t.Fatalf("NewDualRecorder: %v", err)
	}
	ev := sampleEvent(t)
	err = dual.Record(context.Background(), ev)
	if err == nil {
		t.Fatal("Record: expected error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("Record: error %v does not wrap sentinel %v", err, sentinel)
	}
	if got := len(secondary.Events()); got != 1 {
		t.Fatalf("secondary recorder: want 1 event despite primary failure, got %d", got)
	}
	if primary.calls != 1 {
		t.Fatalf("primary recorder: want 1 call, got %d", primary.calls)
	}
}

func TestDualReturnsBothErrors(t *testing.T) {
	primaryErr := errors.New("primary boom")
	secondaryErr := errors.New("secondary boom")
	primary := &failingRecorder{Sentinel: primaryErr}
	secondary := &failingRecorder{Sentinel: secondaryErr}
	dual, err := NewDualRecorder(primary, secondary)
	if err != nil {
		t.Fatalf("NewDualRecorder: %v", err)
	}
	err = dual.Record(context.Background(), sampleEvent(t))
	if err == nil {
		t.Fatal("Record: expected error, got nil")
	}
	if !errors.Is(err, primaryErr) {
		t.Fatalf("Record: error %v does not wrap primary error %v", err, primaryErr)
	}
	if !errors.Is(err, secondaryErr) {
		t.Fatalf("Record: error %v does not wrap secondary error %v", err, secondaryErr)
	}
}

func TestDualPropagatesContextCancellation(t *testing.T) {
	primary := &observingRecorder{}
	secondary := &observingRecorder{}
	dual, err := NewDualRecorder(primary, secondary)
	if err != nil {
		t.Fatalf("NewDualRecorder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = dual.Record(ctx, sampleEvent(t))
	if err == nil {
		t.Fatal("Record: expected error from cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Record: error %v does not wrap context.Canceled", err)
	}
	if primary.calls != 1 {
		t.Fatalf("primary recorder: want 1 call, got %d", primary.calls)
	}
	if secondary.calls != 1 {
		t.Fatalf("secondary recorder: want 1 call, got %d", secondary.calls)
	}
	if !errors.Is(primary.ctxErrs[0], context.Canceled) {
		t.Fatalf("primary recorder: expected ctx.Err to be Canceled, got %v", primary.ctxErrs[0])
	}
	if !errors.Is(secondary.ctxErrs[0], context.Canceled) {
		t.Fatalf("secondary recorder: expected ctx.Err to be Canceled, got %v", secondary.ctxErrs[0])
	}
}

func TestDualNilRecorderRejected(t *testing.T) {
	if _, err := NewDualRecorder(nil, NewMemoryRecorder()); err == nil {
		t.Fatal("NewDualRecorder: expected error for nil primary")
	}
	if _, err := NewDualRecorder(NewMemoryRecorder(), nil); err == nil {
		t.Fatal("NewDualRecorder: expected error for nil secondary")
	}
}
