//go:build integration

package audit

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestPartmanAdoptionPreservesLedgerAndMaintainsHeadroom proves migration 004
// adopts the pre-existing ledger without moving rows and gives audit_writer the
// narrow maintenance capability used by the production worker.
func TestPartmanAdoptionPreservesLedgerAndMaintainsHeadroom(t *testing.T) {
	fixture := newPartmanTestDatabase(t)
	fixture.migrateTo(t, 3)
	ctx := context.Background()

	legacyPartitionCount := fixture.partitionCount(t, `^events_[0-9]{4}_[0-9]{2}_[0-9]{2}$`)
	if legacyPartitionCount == 0 {
		t.Fatalf(
			"migration 003 has no hand-named audit.events partitions; total children = %d",
			fixture.partitionCount(t, `.*`),
		)
	}

	eventID := uuid.Must(uuid.NewV7())
	fixture.insertAuditEvent(t, eventID, time.Now().UTC())
	fixture.migrateTo(t, 4)

	var premake int
	var retention *string
	var retentionKeepTable bool
	var infiniteTimePartitions bool
	if err := fixture.pool.QueryRow(ctx, `
		SELECT premake, retention, retention_keep_table, infinite_time_partitions
		FROM partman.part_config
		WHERE parent_table = 'audit.events'
	`).Scan(
		&premake,
		&retention,
		&retentionKeepTable,
		&infiniteTimePartitions,
	); err != nil {
		t.Fatalf("part_config query: %v", err)
	}
	if premake != 12 {
		t.Fatalf("premake = %d, want 12", premake)
	}
	if retention != nil {
		t.Fatalf("retention = %q, want NULL", *retention)
	}
	if !retentionKeepTable {
		t.Fatal("retention_keep_table = false, want true")
	}
	if !infiniteTimePartitions {
		t.Fatal("infinite_time_partitions = false, want true")
	}

	if nativeCount := fixture.partitionCount(
		t,
		`^events_p[0-9]{4}_[0-9]{2}_[0-9]{2}$`,
	); nativeCount < legacyPartitionCount {
		t.Fatalf(
			"native partition count = %d, want at least %d",
			nativeCount,
			legacyPartitionCount,
		)
	}
	if oldCount := fixture.partitionCount(
		t,
		`^events_[0-9]{4}_[0-9]{2}_[0-9]{2}$`,
	); oldCount != 0 {
		t.Fatalf("hand-named partition count after adoption = %d, want 0", oldCount)
	}

	var preservedRows int
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT count(*) FROM audit.events WHERE event_id = $1`,
		eventID,
	).Scan(&preservedRows); err != nil {
		t.Fatalf("preserved row query: %v", err)
	}
	if preservedRows != 1 {
		t.Fatalf("preserved rows = %d, want 1", preservedRows)
	}

	assertPartitionFunctionSecurity(t, fixture)
	beforeShortage := fixture.futurePartitionCount(t)
	fixture.dropLatestEmptyFuturePartition(t)
	shortageCount := fixture.futurePartitionCount(t)
	if shortageCount >= beforeShortage {
		t.Fatalf(
			"future partition count after shortage = %d, want below %d",
			shortageCount,
			beforeShortage,
		)
	}

	connection, err := fixture.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire writer connection: %v", err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `SET ROLE audit_writer`); err != nil {
		t.Fatalf("set role audit_writer: %v", err)
	}
	if _, err := connection.Exec(ctx, `SELECT audit.run_partition_maintenance()`); err != nil {
		t.Fatalf("run maintenance as audit_writer: %v", err)
	}
	if _, err := connection.Exec(ctx, `RESET ROLE`); err != nil {
		t.Fatalf("reset role: %v", err)
	}

	maintainedCount := fixture.futurePartitionCount(t)
	if maintainedCount <= shortageCount {
		t.Fatalf(
			"future partition count after maintenance = %d, want above %d",
			maintainedCount,
			shortageCount,
		)
	}
	fixture.assertPartitionCovers(t, time.Now().UTC().AddDate(0, 0, 84))
}

func assertPartitionFunctionSecurity(t *testing.T, fixture *partmanTestDatabase) {
	t.Helper()
	var owner string
	var securityDefiner bool
	var searchPath string
	var writerCanExecute bool
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT pg_get_userbyid(p.proowner),
		       p.prosecdef,
		       array_to_string(p.proconfig, ','),
		       has_function_privilege(
		           'audit_writer',
		           'audit.run_partition_maintenance()',
		           'EXECUTE'
		       )
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'audit'
		  AND p.proname = 'run_partition_maintenance'
	`).Scan(&owner, &securityDefiner, &searchPath, &writerCanExecute); err != nil {
		t.Fatalf("maintenance function query: %v", err)
	}
	if owner != fixture.migrationOwner {
		t.Fatalf("function owner = %q, want %q", owner, fixture.migrationOwner)
	}
	if !securityDefiner {
		t.Fatal("function SECURITY DEFINER = false, want true")
	}
	const expectedSearchPath = "search_path=pg_catalog, partman, audit"
	if searchPath != expectedSearchPath {
		t.Fatalf("function search_path = %q, want %q", searchPath, expectedSearchPath)
	}
	if !writerCanExecute {
		t.Fatal("audit_writer EXECUTE privilege = false, want true")
	}
}
