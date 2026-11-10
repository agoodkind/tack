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

// recordWithin runs Record with a context without a deadline and returns its
// error. It fails the test if Record does not return within stallLimit.
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

// TestBufferedWithoutOverflowRefusesWhileRetainedEventsFillTheQueue checks
// TACK-583. Without overflow, Record formerly waited under the stop lock when
// the queue filled. Retained events kept the queue full during an outage and
// blocked that Record, later Record calls, and Close. Record must refuse the
// event immediately, and Close must return.
func TestBufferedWithoutOverflowRefusesWhileRetainedEventsFillTheQueue(t *testing.T) {
	logs := captureSyncedLogs(t)
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, retainTopic))
	if err != nil {
		t.Fatalf("kfake.NewCluster: %v", err)
	}

	t.Cleanup(cluster.Close)
	stop := refuseProduces(cluster)
	t.Cleanup(stop)
	buffered := audit.NewBufferedRecorder(
		context.Background(),
		newRetainKafka(t, cluster),
		nil,
		audit.BufferedConfig{Capacity: 1, BatchSize: 1, FlushInterval: 20 * time.Millisecond},
	)

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
