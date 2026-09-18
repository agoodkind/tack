package audit

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kfake"
	"goodkind.io/tack/internal/telemetry"
)

// gatedRecorder holds every delivery until release is called, then hands it
// to the real recorder behind it. It is what a slow broker looks like to the
// flusher, without holding the fake broker itself, whose held connection
// would also hold the test's own consumer.
type gatedRecorder struct {
	inner *SpillRecorder
	gate  chan struct{}
	once  sync.Once
}

func newGatedRecorder(inner *SpillRecorder) *gatedRecorder {
	return &gatedRecorder{inner: inner, gate: make(chan struct{}), once: sync.Once{}}
}

func (g *gatedRecorder) release() { g.once.Do(func() { close(g.gate) }) }

func (g *gatedRecorder) Record(ctx context.Context, ev Event) error {
	<-g.gate
	return g.inner.Record(ctx, ev)
}

func (g *gatedRecorder) RecordBatch(ctx context.Context, events []Event) ([]Event, error) {
	<-g.gate
	return g.inner.RecordBatch(ctx, events)
}

func (g *gatedRecorder) Close() error { return g.inner.Close() }

func newBufferedForTest(t *testing.T, cluster *kfake.Cluster, outbox *memoryOutbox, cfg BufferedConfig) *BufferedRecorder {
	t.Helper()
	inner := &SpillRecorder{Primary: newKafkaRecorderForTest(t, cluster), Spill: outbox}
	buffered := NewBufferedRecorder(context.Background(), inner, outbox, cfg)
	t.Cleanup(func() { _ = buffered.Close() })
	return buffered
}

// TestBufferedRecordAnswersReadsBeforeTheBroker is criterion 12's produce
// half: a read-class event costs the request no synchronous produce, and a
// state change still costs exactly one.
func TestBufferedRecordAnswersReadsBeforeTheBroker(t *testing.T) {
	cluster := newFakeCluster(t)
	outbox := &memoryOutbox{}
	buffered := newBufferedForTest(t, cluster, outbox, BufferedConfig{Capacity: 0, BatchSize: 0, FlushInterval: 50 * time.Millisecond})
	ctx, counts := telemetry.WithRequestPath(context.Background())

	const reads = 20
	for i := range reads {
		if err := buffered.Record(ctx, makeEvent("read-"+string(rune('a'+i)))); err != nil {
			t.Fatalf("record read %d: %v", i, err)
		}
	}
	if got := counts.Counts().SyncProduces; got != 0 {
		t.Fatalf("reads cost %d synchronous produces, want 0", got)
	}
	if err := buffered.Record(ctx, spillTestEvent()); err != nil {
		t.Fatalf("record state change: %v", err)
	}
	if got := counts.Counts().SyncProduces; got != 1 {
		t.Fatalf("a state change cost %d synchronous produces, want 1", got)
	}
	if records := consumeRecords(t, cluster.ListenAddrs(), testKafkaTopic, reads+1); len(records) != reads+1 {
		t.Fatalf("broker holds %d records, want %d", len(records), reads+1)
	}
	if outbox.count() != 0 {
		t.Fatalf("outbox holds %d events while the broker answered", outbox.count())
	}
}

// TestBufferedFlushSpillsWhatTheBrokerRefuses pins that a refused batch is
// not a lost batch: every event lands in the outbox for the relay.
func TestBufferedFlushSpillsWhatTheBrokerRefuses(t *testing.T) {
	cluster := newFakeCluster(t)
	stop := refuseProduce(cluster)
	t.Cleanup(stop)
	outbox := &memoryOutbox{}
	buffered := newBufferedForTest(t, cluster, outbox, BufferedConfig{Capacity: 0, BatchSize: 0, FlushInterval: 50 * time.Millisecond})

	const reads = 5
	for i := range reads {
		if err := buffered.Record(context.Background(), makeEvent("refused-"+string(rune('a'+i)))); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	waitUntil(t, 10*time.Second, "refused reads never reached the outbox", func() bool {
		return outbox.count() == reads
	})
}

// TestBufferedCloseDrainsTheQueue pins the shutdown contract: what was queued
// when Close ran is on the broker when Close returns.
func TestBufferedCloseDrainsTheQueue(t *testing.T) {
	cluster := newFakeCluster(t)
	outbox := &memoryOutbox{}
	inner := &SpillRecorder{Primary: newKafkaRecorderForTest(t, cluster), Spill: outbox}
	buffered := NewBufferedRecorder(context.Background(), inner, outbox, BufferedConfig{Capacity: 0, BatchSize: 1000, FlushInterval: time.Hour})

	const reads = 30
	for i := range reads {
		if err := buffered.Record(context.Background(), makeEvent("queued-"+string(rune('a'+i)))); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if err := buffered.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if records := consumeRecords(t, cluster.ListenAddrs(), testKafkaTopic, reads); len(records) != reads {
		t.Fatalf("broker holds %d records after Close, want %d", len(records), reads)
	}
}

// TestBufferedOverflowGoesToTheOutbox pins that a full queue behind a stalled
// broker never blocks the request and never drops the event: the event goes
// to the outbox at once.
func TestBufferedOverflowGoesToTheOutbox(t *testing.T) {
	cluster := newFakeCluster(t)
	outbox := &memoryOutbox{}
	gated := newGatedRecorder(&SpillRecorder{Primary: newKafkaRecorderForTest(t, cluster), Spill: outbox})
	buffered := NewBufferedRecorder(context.Background(), gated, outbox, BufferedConfig{Capacity: 1, BatchSize: 1, FlushInterval: time.Hour})
	t.Cleanup(func() { gated.release(); _ = buffered.Close() })

	// The flusher takes the first event and waits on the held delivery; the
	// second fills the queue; the third has nowhere to go but the outbox.
	first := makeEvent("held-first")
	if err := buffered.Record(context.Background(), first); err != nil {
		t.Fatalf("record first: %v", err)
	}
	waitUntil(t, 5*time.Second, "the flusher never took the first event", func() bool {
		return len(buffered.queue) == 0
	})
	if err := buffered.Record(context.Background(), makeEvent("held-second")); err != nil {
		t.Fatalf("record second: %v", err)
	}
	overflow := makeEvent("overflow")
	overflow.EventID = uuid.Must(uuid.NewV7())
	start := time.Now()
	if err := buffered.Record(context.Background(), overflow); err != nil {
		t.Fatalf("record overflow: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the overflowing record waited %s on a held delivery", took)
	}
	if outbox.count() != 1 {
		t.Fatalf("outbox holds %d events, want the one overflowed event", outbox.count())
	}
	var stored Event
	if err := json.Unmarshal(outbox.entries[0], &stored); err != nil {
		t.Fatalf("decode the overflowed event: %v", err)
	}
	if stored.EventID != overflow.EventID {
		t.Fatalf("outbox holds event %s, want the overflowed %s", stored.EventID, overflow.EventID)
	}

	// Once delivery is released, the two held events reach the broker.
	gated.release()
	if records := consumeRecords(t, cluster.ListenAddrs(), testKafkaTopic, 2); len(records) != 2 {
		t.Fatalf("broker holds %d records after release, want 2", len(records))
	}
}
