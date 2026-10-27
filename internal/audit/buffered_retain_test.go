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

// TestBufferedRetainsUndeliveredEventsUntilRecovery is TACK-583. On QA the
// broker refused an auth.token_used event, the outbox append failed with
// FoundationDB error 1031, and the flusher discarded the event. The flusher
// must retain such an event, count it against the queue capacity, and deliver
// it exactly once after the broker returns.
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
