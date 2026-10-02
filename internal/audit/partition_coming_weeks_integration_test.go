//go:build integration

package audit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// comingWeeksMigration replaces audit.run_partition_maintenance (TACK-551).
	comingWeeksMigration int64 = 17
	// farChildShard is the chain shard of the event in the far-future child.
	farChildShard int16 = 150
	// partitionWeek is the length of one audit.events partition.
	partitionWeek = 7 * 24 * time.Hour
	// callerTimeZone is behind UTC. A Monday 00:00 UTC week starts on Sunday
	// in this zone. pg_partman builds a week's table name from its start in
	// the session time zone.
	callerTimeZone = "America/Los_Angeles"
)

// farChildEventTime is inside the far-future child that both tests attach.
var farChildEventTime = time.Date(2031, time.March, 5, 12, 0, 0, 0, time.UTC)

// TestPartitionMigrationRefusesStrayChildName attaches a child named outside
// events_pYYYY_MM_DD. Maintenance fails on the name, the migration fails with
// an error that lists the child, and the maintenance function stays as it was.
func TestPartitionMigrationRefusesStrayChildName(t *testing.T) {
	fixture := newPartmanTestDatabase(t)
	fixture.migrateTo(t, comingWeeksMigration-1)
	ctx := context.Background()
	attachFarChild(t, fixture, "events_tack336_proof")
	if err := runPartitionMaintenance(ctx, fixture); err == nil || !strings.Contains(err.Error(), `invalid value "roof"`) {
		t.Fatalf("maintenance with events_tack336_proof = %v, want invalid value \"roof\"", err)
	}

	err := fixture.migrate(comingWeeksMigration)
	if err == nil || !strings.Contains(err.Error(), "events_tack336_proof") {
		t.Fatalf("migration %d with events_tack336_proof = %v, want an error that lists the child", comingWeeksMigration, err)
	}
	var appliedVersion int64
	if err := fixture.pool.QueryRow(ctx, `
		SELECT max(version_id) FROM goose_db_version WHERE is_applied
	`).Scan(&appliedVersion); err != nil {
		t.Fatalf("read applied migration version: %v", err)
	}
	if appliedVersion != comingWeeksMigration-1 {
		t.Fatalf("applied migration version = %d, want %d", appliedVersion, comingWeeksMigration-1)
	}
	if source := maintenanceSource(t, fixture); strings.Contains(source, "create_partition_time") {
		t.Fatalf("maintenance function changed after the refused migration:\n%s", source)
	}
}

// TestPartitionMaintenanceCreatesComingWeeksPastFarChild drops the current
// week and the two weeks after it, then attaches a 2031 child with one
// chained event. Before migration 17, maintenance creates none of the three
// weeks and a current-week write fails. After it, maintenance called from an
// America/Los_Angeles session creates every week under its Monday UTC name,
// recreates a dropped week, and the chain still verifies.
func TestPartitionMaintenanceCreatesComingWeeksPastFarChild(t *testing.T) {
	fixture := newPartmanTestDatabase(t)
	fixture.migrateTo(t, comingWeeksMigration-1)
	ctx := context.Background()
	now := time.Now().UTC()
	weeks := []time.Time{now, now.Add(partitionWeek), now.Add(2 * partitionWeek)}
	for _, week := range weeks {
		dropPartitionsCovering(t, fixture.pool, week)
	}
	attachFarChild(t, fixture, "events_p2031_03_03")
	orgID := appendFarChildEvent(t, fixture)

	if err := runPartitionMaintenance(ctx, fixture); err != nil {
		t.Fatalf("maintenance before migration %d: %v", comingWeeksMigration, err)
	}
	for _, week := range weeks {
		if covering := partitionCountCovering(t, fixture.pool, week); covering != 0 {
			t.Fatalf("partitions covering %s before migration %d = %d, want 0", week.Format(time.DateOnly), comingWeeksMigration, covering)
		}
	}
	insertErr := insertPartitionManagerTestEvent(ctx, fixture.pool, uuid.Must(uuid.NewV7()), now)
	var postgresError *pgconn.PgError
	if !errors.As(insertErr, &postgresError) || postgresError.Code != "23514" {
		t.Fatalf("current-week insert before migration %d = %v, want SQLSTATE 23514", comingWeeksMigration, insertErr)
	}

	fixture.migrateTo(t, comingWeeksMigration)
	requireMaintenanceSettings(t, fixture)
	for run := 1; run <= 2; run++ {
		if err := runPartitionMaintenanceIn(ctx, fixture, callerTimeZone); err != nil {
			t.Fatalf("maintenance run %d after migration %d: %v", run, comingWeeksMigration, err)
		}
	}
	requireWeeksCovered(t, fixture, weeks)
	requireUTCWeekNames(t, fixture, weeks)
	if err := insertPartitionManagerTestEvent(ctx, fixture.pool, uuid.Must(uuid.NewV7()), weeks[1]); err != nil {
		t.Fatalf("next-week insert after migration %d: %v", comingWeeksMigration, err)
	}

	dropped := weeks[2]
	dropPartitionsCovering(t, fixture.pool, dropped)
	if err := runPartitionMaintenanceIn(ctx, fixture, callerTimeZone); err != nil {
		t.Fatalf("maintenance after dropping %s: %v", dropped.Format(time.DateOnly), err)
	}
	requireWeeksCovered(t, fixture, weeks)
	requireUTCWeekNames(t, fixture, weeks)
	if covering := partitionCountCovering(t, fixture.pool, farChildEventTime); covering != 1 {
		t.Fatalf("partitions covering %s = %d, want the 2031 child", farChildEventTime.Format(time.DateOnly), covering)
	}
	requireChainVerifies(t, fixture, orgID)
}
