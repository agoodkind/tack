//go:build runtimeaudit

package integration_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// ledgerStorage stores YugabyteDB size measurements for the audit tables. The
// audit tables are the tables in the audit schema, including each audit.events
// partition, and public.ops_outbox, public.ops_outbox_parts, and
// public.ops_read_state.
type ledgerStorage struct {
	indexBytes      int64
	totalBytes      int64
	eventTableBytes int64
	events          int64
	partTableBytes  int64
	parts           int64
}

// readLedgerStorage returns pg_indexes_size and pg_total_relation_size sums
// for the audit tables, plus the total size and row count of audit.events and
// of audit.event_parts.
func readLedgerStorage(t *testing.T, admin *pgxpool.Pool) ledgerStorage {
	t.Helper()
	var storage ledgerStorage
	err := admin.QueryRow(t.Context(), readTestSQL(t, "runtime_audit_scaling_storage.sql")).Scan(
		&storage.indexBytes, &storage.totalBytes, &storage.eventTableBytes, &storage.events,
		&storage.partTableBytes, &storage.parts)
	if err != nil {
		t.Fatalf("Reading the YugabyteDB audit table sizes failed; the storage measurement requires them: %v", err)
	}
	return storage
}

// perUnit divides total by count and returns zero when count is zero.
func perUnit(total, count int64) float64 {
	if count == 0 {
		return 0
	}
	return float64(total) / float64(count)
}

// outboxRecordCount counts rows and parts in the YugabyteDB operator
// outbox.
func outboxRecordCount(t *testing.T, admin *pgxpool.Pool) int64 {
	t.Helper()
	var records int64
	if err := admin.QueryRow(t.Context(), readTestSQL(t, "runtime_audit_outbox_records.sql")).Scan(&records); err != nil {
		t.Fatalf("Counting YugabyteDB operator outbox rows and parts failed: %v", err)
	}
	return records
}

// drainEarlierOutbox relays the YugabyteDB operator outbox records that
// earlier tests in this test binary left to a separate topic, and an audit
// consumer that creates that topic stores them in the ledger. It waits until
// the outbox is empty and returns the number of drained records. The scaling
// run invokes it before the workload, and the measured topic then receives
// only the records of that workload.
func drainEarlierOutbox(t *testing.T, clusterFile string) int64 {
	t.Helper()
	admin := ledgerAdminPool(t)
	earlier := outboxRecordCount(t, admin)
	if earlier == 0 {
		return 0
	}
	topic := "audit.scaling-drain-" + uuid.NewString()[:8]
	startScalingConsumer(t, topic, "tack-scaling-drain-"+uuid.NewString()[:8])
	opened, err := auditrelay.Open(t.Context(), auditrelay.Config{
		Brokers: []string{testenv.Kafka(t)}, Topic: topic,
		YugabyteDSN: testenv.Ledger(t), FDBClusterFile: clusterFile, FDBTransactionTimeout: 5 * time.Second,
		PollInterval: 100 * time.Millisecond, BatchSize: scalingRelayBatch,
	})
	if err != nil {
		t.Fatalf("Opening a relay for the earlier YugabyteDB outbox records failed: %v", err)
	}
	defer opened.Pool.Close()
	opened.Relay.Start(t.Context())
	deadline := scalingDeadline()
	remaining := earlier
	for remaining > 0 && clock.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		remaining = outboxRecordCount(t, admin)
	}
	if err := opened.Relay.Close(); err != nil {
		t.Fatalf("Closing the relay for the earlier YugabyteDB outbox records failed: %v", err)
	}
	if remaining > 0 {
		t.Fatalf("The relay did not drain the earlier YugabyteDB outbox records before the deadline: %d of %d remain", remaining, earlier)
	}
	return earlier
}
