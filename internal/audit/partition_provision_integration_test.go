//go:build integration

package audit

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/migrations"
)

// One maintenance pass leaves two or three future weeks. Measured on
// 2026-10-05 00:17 UTC, a Monday: two. Measured on 2026-10-02, a Friday:
// three.
const maxHeadroomWeeks = partitionHeadroomAlertFloor + 1

var partmanChildName = regexp.MustCompile(`^events_p[0-9]{4}_[0-9]{2}_[0-9]{2}$`)

// TestFreshProvisionMaintainsWeeklyPartitions applies every migration to an
// empty ledger through postgres.Migrate, the call ops provision makes, and
// runs one partition-manager pass as an audit_writer login, the role the
// audit consumer uses. The pass must succeed, leave every week through the
// last premade one partitioned under pg_partman names, and report the
// headroom pg_partman produces, with no manual SQL.
func TestFreshProvisionMaintainsWeeklyPartitions(t *testing.T) {
	fixture := newPartmanTestDatabase(t)
	ctx := context.Background()
	fixture.pool.Close()
	if err := postgres.Migrate(ctx, fixture.dsn, migrations.FS); err != nil {
		t.Fatalf("provision migrations: %v", err)
	}
	if err := fixture.reopenPool(); err != nil {
		t.Fatalf("reopen fixture pool: %v", err)
	}
	var version int64
	if err := fixture.pool.QueryRow(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil {
		t.Fatalf("read applied migration version: %v", err)
	}
	if latest := latestMigrationVersion(t); version != latest {
		t.Fatalf("applied migration version = %d, want %d", version, latest)
	}

	t.Log("audit.events children after migrations, before the partition-manager pass:")
	eventsChildNames(t, fixture.pool)

	store := NewPGPartitionStore(auditWriterPool(t, fixture))
	okBefore := maintenanceOutcomes(t, "ok")
	errorsBefore := maintenanceOutcomes(t, "error")
	logs := &lockedBuffer{mu: sync.Mutex{}, buf: bytes.Buffer{}}
	handler := slog.NewJSONHandler(logs, &slog.HandlerOptions{AddSource: false, Level: slog.LevelDebug, ReplaceAttr: nil})
	NewPartitionManager(store, time.Hour).runOnce(telemetry.WithLogger(ctx, slog.New(handler)))

	children := eventsChildNames(t, fixture.pool)
	output := logs.String()
	if maintenanceOutcomes(t, "ok") != okBefore+1 || maintenanceOutcomes(t, "error") != errorsBefore ||
		!strings.Contains(output, `"msg":"audit.partition.maintained"`) {
		t.Fatalf("partition-manager pass did not complete maintenance without error; logs:\n%s", output)
	}
	now := time.Now().UTC()
	headroom, err := store.HeadroomWeeks(ctx, now)
	if err != nil {
		t.Fatalf("read headroom: %v", err)
	}
	if gauge := headroomGauge(t); gauge != int64(headroom) {
		t.Fatalf("headroom gauge after the pass = %d, want the HeadroomWeeks value %d", gauge, headroom)
	}
	if headroom < partitionHeadroomAlertFloor {
		t.Fatalf("HeadroomWeeks = %d, below the alert floor %d", headroom, partitionHeadroomAlertFloor)
	}
	if headroom > maxHeadroomWeeks {
		t.Fatalf("HeadroomWeeks = %d, want at most %d", headroom, maxHeadroomWeeks)
	}
	for week := 0; week <= headroom; week++ {
		target := now.AddDate(0, 0, 7*week)
		if covering := partitionCountCovering(t, fixture.pool, target); covering != 1 {
			t.Fatalf("partitions covering %s (week +%d) = %d, want 1", target.Format(time.DateOnly), week, covering)
		}
	}
	for _, name := range children {
		if !partmanChildName.MatchString(name) {
			t.Fatalf("audit.events child %q does not match %s", name, partmanChildName)
		}
	}
}

func eventsChildNames(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT c.relname, pg_get_expr(c.relpartbound, c.oid)
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = 'audit' AND p.relname = 'events'
		ORDER BY c.relname
	`)
	if err != nil {
		t.Fatalf("list audit.events children: %v", err)
	}
	var names []string
	for rows.Next() {
		var name, bound string
		if err := rows.Scan(&name, &bound); err != nil {
			rows.Close()
			t.Fatalf("read audit.events child: %v", err)
		}
		t.Logf("audit.events child %s %s", name, bound)
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit.events children: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("audit.events has no child partitions")
	}
	return names
}
