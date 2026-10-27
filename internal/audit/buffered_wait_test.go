package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"goodkind.io/tack/internal/audit"
)

// stallLimit is the longest a refused Record or Close may take before the
// test treats it as stalled.
const stallLimit = 5 * time.Second

// recordWithin runs Record with a context that has no deadline and returns
// its error, or fails the test when Record does not return within stallLimit.
func recordWithin(t *testing.T, buffered *audit.BufferedRecorder, ev audit.Event) error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- buffered.Record(context.Background(), ev) }()
	select {
	case err := <-result:
		return err
	case <-time.After(stallLimit):
		t.Fatalf("Record of %s did not return within %s", ev.EventID, stallLimit)
		return nil
	}
}

// TestBufferedWithoutOverflowRefusesWhileRetainedEventsFillTheQueue is
// TACK-583. With no overflow, a Record that found the queue full used to
// wait under the stop lock until room opened. Retained events keep the queue
// full for the whole outage. That Record, every later Record, and Close then
// stalled. Record must refuse the event at once, and Close must return.
func TestBufferedWithoutOverflowRefusesWhileRetainedEventsFillTheQueue(t *testing.T) {
	logs := captureSyncedLogs(t)
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, retainTopic))
	if err != nil {
		t.Fatalf("kfake.NewCluster: %v", err)
	}
	t.Cleanup(cluster.Close)
	stop := refuseProduces(cluster)
	t.Cleanup(stop)
	buffered := audit.NewBufferedRecorder(context.Background(), newRetainKafka(t, cluster), nil,
		audit.BufferedConfig{Capacity: 1, BatchSize: 1, FlushInterval: 20 * time.Millisecond})

	if err := buffered.Record(context.Background(), retainReadEvent("retained")); err != nil {
		t.Fatalf("record: %v", err)
	}
	logs.waitFor(t, "audit.buffer.delivery_retained")
	for _, suffix := range []string{"first-refused", "later-refused"} {
		if err := recordWithin(t, buffered, retainReadEvent(suffix)); err == nil {
			t.Fatalf("Record accepted %s while the retained event filled the queue", suffix)
		}
	}

	stop()
	closed := make(chan error, 1)
	go func() { closed <- buffered.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(stallLimit):
		t.Fatalf("Close did not return within %s", stallLimit)
	}
}
