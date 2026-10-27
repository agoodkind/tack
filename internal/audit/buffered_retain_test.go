package audit_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"goodkind.io/tack/internal/audit"
)

// TestBufferedRetainsUndeliveredEventsUntilRecovery is TACK-583. The test
// retains a read-class event when the broker refuses production and the
// outbox append fails. It requires the flusher to retain the event, count it
// against queue capacity, and deliver it exactly once after broker recovery.
func TestBufferedRetainsUndeliveredEventsUntilRecovery(t *testing.T) {
	logs := captureSyncedLogs(t)
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, retainTopic))
	if err != nil {
		t.Fatalf("kfake.NewCluster: %v", err)
	}
	t.Cleanup(cluster.Close)
	stop := refuseProduces(cluster)
	t.Cleanup(stop)
	outbox := &failingOutbox{mu: sync.Mutex{}, fail: nil, entries: nil}
	outbox.setFail(errors.New("FoundationDB error 1031: transaction timed out"))
	inner := &audit.SpillRecorder{Primary: newRetainKafka(t, cluster), Spill: outbox}
	buffered := audit.NewBufferedRecorder(context.Background(), inner, outbox,
		audit.BufferedConfig{Capacity: 1, BatchSize: 1, FlushInterval: 20 * time.Millisecond})
	t.Cleanup(func() { _ = buffered.Close() })
	ctx := context.Background()

	retained := retainReadEvent("retained")
	if err := buffered.Record(ctx, retained); err != nil {
		t.Fatalf("record: %v", err)
	}
	logs.waitFor(t, "audit.buffer.delivery_retained")
	if err := buffered.Record(ctx, retainReadEvent("refused")); err == nil {
		t.Fatal("Record accepted an event while the retained event filled the capacity and the outbox failed")
	}

	stop()
	outbox.setFail(nil)
	logs.waitFor(t, "audit.buffer.retained_delivered")
	if err := buffered.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The sentinel follows every buffered delivery on the single partition. A
	// duplicate or an extra delivery would place another record before it.
	sentinel := retainReadEvent("sentinel")
	if err := newRetainKafka(t, cluster).Record(ctx, sentinel); err != nil {
		t.Fatalf("record sentinel: %v", err)
	}
	deliveries := consumeEventIDs(t, cluster, 2)
	for _, ev := range []audit.Event{retained, sentinel} {
		if got := deliveries[ev.EventID]; got != 1 {
			t.Fatalf("broker stored event %s %d times, want exactly once; deliveries %v", ev.EventID, got, deliveries)
		}
	}
}

// TestBufferedRetainLogStatesTheRecordError is TACK-583: when the inner
// recorder records one event at a time, the retained-event log states the
// error that the inner recorder returned for each event and that event's ID.
func TestBufferedRetainLogStatesTheRecordError(t *testing.T) {
	logs := captureSyncedLogs(t)
	// The test does not call Close: the unwired recorder refuses every
	// retry, and Close would retry for the whole close deadline.
	buffered := audit.NewBufferedRecorder(context.Background(), audit.UnwiredRecorder{}, nil,
		audit.BufferedConfig{Capacity: 1, BatchSize: 1, FlushInterval: 20 * time.Millisecond})

	retained := retainReadEvent("unwired")
	if err := buffered.Record(context.Background(), retained); err != nil {
		t.Fatalf("record: %v", err)
	}
	logs.waitFor(t, "audit.buffer.delivery_retained")
	for _, want := range []string{audit.ErrRecorderUnwired.Error(), retained.EventID.String()} {
		if !logs.lineContains("audit.buffer.delivery_retained", want) {
			t.Fatalf("audit.buffer.delivery_retained does not state %q", want)
		}
	}
}
