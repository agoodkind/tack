package audit

import (
	"context"
	"testing"
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

func TestSuppressingRecorderCloseClosesInner(t *testing.T) {
	inner := &closingRecorder{}
	recorder := SuppressingRecorder{Inner: inner}

	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !inner.closed {
		t.Fatal("inner Close() was not called")
	}
}

func TestSuppressingRecorderCloseWithoutInnerCloser(t *testing.T) {
	recorder := SuppressingRecorder{Inner: openRecorder{}}

	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestSuppressingRecorderCloseClosesInnerWithoutError(t *testing.T) {
	inner := &plainClosingRecorder{}
	recorder := SuppressingRecorder{Inner: inner}

	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !inner.closed {
		t.Fatal("inner Close() was not called")
	}
}
