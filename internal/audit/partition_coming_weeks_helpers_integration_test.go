//go:build integration

package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// attachFarChild attaches a 2031 week to audit.events under name.
func attachFarChild(t *testing.T, fixture *partmanTestDatabase, name string) {
	t.Helper()
	statement := fmt.Sprintf(`
		CREATE TABLE %s PARTITION OF audit.events
		FOR VALUES FROM ('2031-03-03 00:00:00+00') TO ('2031-03-10 00:00:00+00')
	`, pgx.Identifier{"audit", name}.Sanitize())
	if _, err := fixture.pool.Exec(context.Background(), statement); err != nil {
		t.Fatalf("attach far child %s: %v", name, err)
	}
}

// appendFarChildEvent appends one chained event inside the far child through
// the production chain writer and returns its organization.
func appendFarChildEvent(t *testing.T, fixture *partmanTestDatabase) uuid.UUID {
	t.Helper()
	orgID := uuid.Must(uuid.NewV7())
	event := chainTestEvent(t, orgID, farChildShard)
	event.Event.OccurredAt = farChildEventTime
	if err := appendWithRetry(context.Background(), fixture.pool, event); err != nil {
		t.Fatalf("append far child event: %v", err)
	}
	return orgID
}

// runPartitionMaintenance calls the function the audit consumer runs on boot
// and daily.
func runPartitionMaintenance(ctx context.Context, fixture *partmanTestDatabase) error {
	if _, err := fixture.pool.Exec(ctx, `SELECT audit.run_partition_maintenance()`); err != nil {
		return fmt.Errorf("run audit partition maintenance: %w", err)
	}
	return nil
}

// maintenanceSource returns the body of audit.run_partition_maintenance.
func maintenanceSource(t *testing.T, fixture *partmanTestDatabase) string {
	t.Helper()
	var source string
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT p.prosrc
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'audit' AND p.proname = 'run_partition_maintenance'
	`).Scan(&source); err != nil {
		t.Fatalf("read maintenance function source: %v", err)
	}
	return source
}

// requireWeeksCovered requires exactly one partition to cover each moment.
func requireWeeksCovered(t *testing.T, fixture *partmanTestDatabase, moments []time.Time) {
	t.Helper()
	for _, moment := range moments {
		if covering := partitionCountCovering(t, fixture.pool, moment); covering != 1 {
			t.Fatalf("partitions covering %s = %d, want 1", moment.Format(time.DateOnly), covering)
		}
	}
}

// requireChainVerifies exports the organization's ledger rows through Export
// and checks the bundle with VerifyBundle, as the restore drill does. The
// organization has one event, and its chain head must record sequence 1.
func requireChainVerifies(t *testing.T, fixture *partmanTestDatabase, orgID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var headSeq int64
	if err := fixture.pool.QueryRow(ctx, `
		SELECT last_seq FROM audit.chain_heads WHERE org_id = $1 AND shard = $2
	`, orgID, farChildShard).Scan(&headSeq); err != nil {
		t.Fatalf("read chain head of %s: %v", orgID, err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate export key: %v", err)
	}
	directory := t.TempDir()
	manifest, err := Export(ctx, &Reader{pool: fixture.pool}, privateKey, "ed25519:partition-test", QueryFilter{
		OrgID: orgID, Oldest: time.Unix(0, 0).UTC(), Latest: time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC),
		Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 0,
	}, directory)
	if err != nil {
		t.Fatalf("export ledger of %s: %v", orgID, err)
	}
	report, err := VerifyBundle(directory, publicKey)
	if err != nil {
		t.Fatalf("verify ledger bundle of %s: %v", orgID, err)
	}
	if manifest.RowCount != 1 || headSeq != 1 || report.HashMatches != 1 {
		t.Fatalf("export rows %d, chain head %d, hash matches %d; want 1 each", manifest.RowCount, headSeq, report.HashMatches)
	}
	if len(report.ChainBreaks) != 0 || report.ChainGapCount != 0 || report.Err() != nil {
		t.Fatalf("chain verify: breaks %v, gaps %d, verdict %v", report.ChainBreaks, report.ChainGapCount, report.Err())
	}
}
