//go:build integration

package audit

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPartmanMigrationUpDownUpPreservesLedger(t *testing.T) {
	fixture := newPartmanTestDatabase(t)
	fixture.migrateTo(t, 3)

	eventID := uuid.Must(uuid.NewV7())
	fixture.insertAuditEvent(t, eventID, time.Now().UTC())
	fixture.migrateTo(t, 4)
	logPartmanArtifacts(t, fixture)

	fixture.rollbackTo(t, 3)
	fixture.migrateTo(t, 4)

	assertPartmanRegistration(t, fixture)
	var preservedRows int
	if err := fixture.pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM audit.events WHERE event_id = $1`,
		eventID,
	).Scan(&preservedRows); err != nil {
		t.Fatalf("preserved row query after up-down-up: %v", err)
	}
	if preservedRows != 1 {
		t.Fatalf("preserved rows after up-down-up = %d, want 1", preservedRows)
	}
}

func TestPartmanMigrationRetriesExistingRegistration(t *testing.T) {
	fixture := newPartmanTestDatabase(t)
	fixture.migrateTo(t, 3)

	eventID := uuid.Must(uuid.NewV7())
	fixture.insertAuditEvent(t, eventID, time.Now().UTC())
	fixture.migrateTo(t, 4)
	if _, err := fixture.pool.Exec(
		context.Background(),
		`DELETE FROM goose_db_version WHERE version_id = 4`,
	); err != nil {
		t.Fatalf("remove migration 004 version marker: %v", err)
	}

	fixture.migrateTo(t, 4)

	assertPartmanRegistration(t, fixture)
	var preservedRows int
	if err := fixture.pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM audit.events WHERE event_id = $1`,
		eventID,
	).Scan(&preservedRows); err != nil {
		t.Fatalf("preserved row query after retry: %v", err)
	}
	if preservedRows != 1 {
		t.Fatalf("preserved rows after retry = %d, want 1", preservedRows)
	}
}

func assertPartmanRegistration(t *testing.T, fixture *partmanTestDatabase) {
	t.Helper()
	var registrationCount int
	var premake int
	var retention *string
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT count(*), min(premake), min(retention)
		FROM partman.part_config
		WHERE parent_table = 'audit.events'
	`).Scan(&registrationCount, &premake, &retention); err != nil {
		t.Fatalf("partman registration query: %v", err)
	}
	if registrationCount != 1 {
		t.Fatalf("partman registration count = %d, want 1", registrationCount)
	}
	if premake != 12 {
		t.Fatalf("partman premake = %d, want 12", premake)
	}
	if retention != nil {
		t.Fatalf("partman retention = %q, want NULL", *retention)
	}
	assertPartitionFunctionSecurity(t, fixture)
}

func logPartmanArtifacts(t *testing.T, fixture *partmanTestDatabase) {
	t.Helper()
	var defaultPartitionCount int
	var templateTableCount int
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = 'audit'
		  AND p.relname = 'events'
		  AND pg_get_expr(c.relpartbound, c.oid) = 'DEFAULT'
	`).Scan(&defaultPartitionCount); err != nil {
		t.Fatalf("default partition inventory: %v", err)
	}
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'partman'
		  AND c.relname LIKE 'template_audit_events%'
	`).Scan(&templateTableCount); err != nil {
		t.Fatalf("template table inventory: %v", err)
	}
	t.Logf(
		"pg_partman artifacts: default_partitions=%d template_tables=%d",
		defaultPartitionCount,
		templateTableCount,
	)
}
