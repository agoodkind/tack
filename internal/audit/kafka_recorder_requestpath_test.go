package audit

import (
	"context"
	"testing"

	"goodkind.io/tack/internal/telemetry"
)

// TestKafkaRecordCountsOneSynchronousProduceOnTheRequest proves the produce
// the recorder blocks on is counted against the request that made it, which
// is the number the request-path criterion reads (TACK-507).
func TestKafkaRecordCountsOneSynchronousProduceOnTheRequest(t *testing.T) {
	cluster := newFakeCluster(t)
	rec := newKafkaRecorderForTest(t, cluster)
	ctx, counts := telemetry.WithRequestPath(context.Background())

	if err := rec.Record(ctx, makeEvent("counted")); err != nil {
		t.Fatalf("record: %v", err)
	}

	if got := counts.Counts().SyncProduces; got != 1 {
		t.Fatalf("sync produces = %d, want 1", got)
	}
	if records := consumeRecords(t, cluster.ListenAddrs(), testKafkaTopic, 1); len(records) != 1 {
		t.Fatalf("broker holds %d records, want 1", len(records))
	}
}
