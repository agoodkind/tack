package integration_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
)

// TestRuntimeAuditReadRedelivery finalizes one read in the FoundationDB product
// read store and one read in the YugabyteDB operator read store. For each read,
// a relay publishes every record of the read to Kafka. The test stops that
// relay after the last Kafka acknowledgement and before the relay deletes the
// outbox records, then closes the consumer. A new relay and a new consumer
// deliver the records again. The test requires one ledger row, one completed
// transport, no transient assembly, an empty outbox, and no dead letters.
func TestRuntimeAuditReadRedelivery(t *testing.T) {
	for _, backend := range readBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			pipeline := newPartsPipeline(t)
			fixture := newReadFixture(t)
			fixture.begin(t, backend.store)
			if err := backend.store.FinalizeRead(t.Context(), fixture.completion); err != nil {
				t.Fatalf("Finalizing the read failed: %v", err)
			}
			eventID := fixture.admission.Token.EventID
			encoded, err := audit.EncodeEvent(fixture.completion.Event, fixture.completion.Targets)
			if err != nil {
				t.Fatalf("Encoding read %s failed: %v", eventID, err)
			}
			records := len(encoded.Parts) + 1

			firstConsumer := pipeline.startConsumer(t)
			hook, stopped := stopAfterAcknowledgements(eventID, records)
			relay := startReadRelay(t, pipeline, backend, hook)
			select {
			case <-stopped:
			case <-time.After(runtimeAuditDeadline):
				t.Fatalf("The relay did not publish %d records of read %s within %s", records, eventID, runtimeAuditDeadline)
			}
			_ = relay.Close()
			if len(backend.terminal(t, eventID)) != 1 {
				t.Fatalf("The %s outbox does not contain read %s after the interrupted delivery", backend.name, eventID)
			}
			pipeline.stack().waitForRow(t, eventID)
			_ = firstConsumer.Close()

			pipeline.startConsumer(t)
			startReadRelay(t, pipeline, backend, nil)
			waitFor(t, "deletion of the outbox records", func() bool { return len(backend.terminal(t, eventID)) == 0 })
			pipeline.waitForConsumed(t)
			for query, want := range map[string]int64{eventRowsQuery: 1, completedRowsQuery: 1, assemblyRowsQuery: 0} {
				if got := pipeline.count(t, query, eventID); got != want {
					t.Fatalf("The query %s returned %d rows for read %s instead of %d", query, got, eventID, want)
				}
			}
			pipeline.stack().waitForDeadLetters(t, 0)
		})
	}
}

// stopAfterAcknowledgements returns an AfterProduce hook and a channel. The hook
// counts Kafka acknowledgements for eventID. After acknowledgement number
// records, the hook closes the channel and returns errStopDelivery, and the
// relay keeps the outbox records.
func stopAfterAcknowledgements(eventID uuid.UUID, records int) (func(context.Context, uuid.UUID) error, <-chan struct{}) {
	var mutex sync.Mutex
	var once sync.Once
	acknowledged := 0
	stopped := make(chan struct{})
	hook := func(_ context.Context, produced uuid.UUID) error {
		mutex.Lock()
		defer mutex.Unlock()
		if produced != eventID {
			return nil
		}
		acknowledged++
		if acknowledged < records {
			return nil
		}
		once.Do(func() { close(stopped) })
		return errStopDelivery
	}
	return hook, stopped
}

// startReadRelay starts the production relay for the outbox of backend on the
// pipeline topic with afterProduce. Test cleanup closes the relay and its pool.
func startReadRelay(t *testing.T, pipeline partsPipeline, backend *readBackend, afterProduce func(context.Context, uuid.UUID) error) *audit.Relay {
	t.Helper()
	opened, err := auditrelay.Open(t.Context(), auditrelay.Config{
		Brokers: []string{pipeline.brokers}, Topic: pipeline.topic, YugabyteDSN: pipeline.ledger,
		FDBClusterFile: backend.clusterFile, FDBTransactionTimeout: 5 * time.Second,
		PollInterval: 100 * time.Millisecond, BatchSize: 64, AfterProduce: afterProduce,
	})
	if err != nil {
		t.Fatalf("Opening the %s relay failed: %v", backend.name, err)
	}
	t.Cleanup(opened.Pool.Close)
	t.Cleanup(func() { _ = opened.Relay.Close() })
	opened.Relay.Start(t.Context())
	return opened.Relay
}
