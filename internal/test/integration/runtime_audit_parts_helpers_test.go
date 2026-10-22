package integration_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// errStopDelivery signals that the AfterProduce hook must stop relay delivery.
var errStopDelivery = errors.New("test stops delivery after the broker acknowledged the record")

// partsPipeline groups a test topic, consumer group, broker address,
// ledger connection string, admin pool, and reader.
type partsPipeline struct {
	topic   string
	group   string
	brokers string
	ledger  string
	admin   *pgxpool.Pool
	reader  *audit.Reader
}

// newPartsPipeline creates a unique topic and consumer group and opens
// ledger connections with test cleanup.
func newPartsPipeline(t *testing.T) partsPipeline {
	t.Helper()
	ledgerDSN := testenv.Ledger(t)
	admin, err := pgxpool.New(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	reader, err := audit.NewReader(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("open the audit reader: %v", err)
	}
	t.Cleanup(reader.Close)
	suffix := uuid.NewString()[:8]
	return partsPipeline{
		topic: "audit.parts-test-" + suffix, group: "tack-parts-test-" + suffix,
		brokers: testenv.Kafka(t), ledger: ledgerDSN, admin: admin, reader: reader,
	}
}

// stack returns a runtimeAuditStack view of the topic, admin pool, and reader.
func (p partsPipeline) stack() runtimeAuditStack {
	return runtimeAuditStack{topic: p.topic, admin: p.admin, reader: p.reader, recorder: nil}
}

// startConsumer starts the production consumer for the test topic and group
// and registers cleanup.
func (p partsPipeline) startConsumer(t *testing.T) *audit.Consumer {
	t.Helper()
	consumer, err := audit.NewConsumer(t.Context(), audit.ConsumerConfig{
		Brokers: []string{p.brokers}, Topic: p.topic, GroupID: p.group,
		BatchSize: 32, PollInterval: 100 * time.Millisecond, YugabyteDSN: p.ledger,
	})
	if err != nil {
		t.Fatalf("start the audit consumer: %v", err)
	}
	consumer.Start(t.Context())
	t.Cleanup(func() { _ = consumer.Close() })
	return consumer
}

// startRelay starts the production relay for the YugabyteDB outbox with the
// given AfterProduce hook and registers cleanup.
func (p partsPipeline) startRelay(t *testing.T, afterProduce func(context.Context, uuid.UUID) error) *audit.Relay {
	t.Helper()
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: []string{p.brokers}, Topic: p.topic, ClientID: "tack-parts-test", ProduceTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("open the relay producer: %v", err)
	}
	relay, err := audit.NewRelay(audit.RelayConfig{
		Recorder: recorder, Yugabyte: audit.NewPoolOutbox(p.admin), FoundationDB: nil,
		PollInterval: 100 * time.Millisecond, BatchSize: 64, AfterProduce: afterProduce,
	})
	if err != nil {
		t.Fatalf("open the relay: %v", err)
	}
	relay.Start(t.Context())
	t.Cleanup(func() { _ = relay.Close() })
	return relay
}

// deliverUntilStopped runs a relay until the broker acknowledges the given
// number of records for one event, then closes the relay.
func (p partsPipeline) deliverUntilStopped(t *testing.T, eventID uuid.UUID, records int) {
	t.Helper()
	var mutex sync.Mutex
	var once sync.Once
	acknowledged := 0
	stopped := make(chan struct{})
	relay := p.startRelay(t, func(_ context.Context, produced uuid.UUID) error {
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
	})
	select {
	case <-stopped:
	case <-time.After(runtimeAuditDeadline):
		t.Fatalf("relay did not produce %d records of event %s within %s", records, eventID, runtimeAuditDeadline)
	}
	_ = relay.Close()
}

// waitForConsumed waits until the consumer group's committed offsets match
// the topic high-water marks, which mark the end of each partition.
func (p partsPipeline) waitForConsumed(t *testing.T) {
	t.Helper()
	waitFor(t, "consumption of every record", func() bool {
		marks, err := audit.TopicHighWaterMarks(t.Context(), []string{p.brokers}, p.topic)
		if err != nil {
			t.Fatalf("read the high-water marks of %s: %v", p.topic, err)
		}
		offsets, err := p.reader.ConsumerOffsets(t.Context())
		if err != nil {
			t.Fatalf("read the consumer offsets: %v", err)
		}
		var produced, consumed int64
		for _, mark := range marks {
			produced += mark
		}
		for _, offset := range offsets {
			if offset.Group == p.group && offset.Topic == p.topic {
				consumed += offset.Offset
			}
		}
		return produced > 0 && produced == consumed
	})
}

// waitFor polls a condition and fails the test if the deadline expires.
func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := clock.Now().Add(runtimeAuditDeadline)
	for !done() {
		if clock.Now().After(deadline) {
			t.Fatalf("%s did not happen within %s", what, runtimeAuditDeadline)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// count runs the count query for one event through the pipeline's admin pool.
func (p partsPipeline) count(t *testing.T, query string, eventID uuid.UUID) int64 {
	t.Helper()
	return countRows(t, p.admin, query, eventID)
}

// countRows runs a count query with one event ID and returns the row count.
func countRows(t *testing.T, pool *pgxpool.Pool, query string, eventID uuid.UUID) int64 {
	t.Helper()
	var total int64
	if err := pool.QueryRow(t.Context(), query, eventID).Scan(&total); err != nil {
		t.Fatalf("count with %q: %v", query, err)
	}
	return total
}
