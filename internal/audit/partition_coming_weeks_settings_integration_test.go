//go:build integration

package audit

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// runPartitionMaintenanceIn opens a new session with TimeZone set to
// timeZone and calls audit.run_partition_maintenance from it. It fails unless
// the session reports that zone before the call.
func runPartitionMaintenanceIn(ctx context.Context, fixture *partmanTestDatabase, timeZone string) error {
	config, err := pgx.ParseConfig(fixture.dsn)
	if err != nil {
		return fmt.Errorf("parse fixture DSN: %w", err)
	}
	config.RuntimeParams["timezone"] = timeZone
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("connect with time zone %s: %w", timeZone, err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var sessionZone string
	if err := conn.QueryRow(ctx, `SHOW TimeZone`).Scan(&sessionZone); err != nil {
		return fmt.Errorf("read session time zone: %w", err)
	}
	if sessionZone != timeZone {
		return fmt.Errorf("session time zone is %s, want %s", sessionZone, timeZone)
	}
	if _, err := conn.Exec(ctx, `SELECT audit.run_partition_maintenance()`); err != nil {
		return fmt.Errorf("run audit partition maintenance in %s: %w", timeZone, err)
	}
	return nil
}

// requireMaintenanceSettings requires the migration owner to own
// audit.run_partition_maintenance as SECURITY DEFINER with its search path
// and a UTC TimeZone, and audit_writer to have EXECUTE on it.
func requireMaintenanceSettings(t *testing.T, fixture *partmanTestDatabase) {
	t.Helper()
	var owner string
	var securityDefiner, writerCanExecute bool
	var settings []string
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT pg_get_userbyid(p.proowner), p.prosecdef, p.proconfig,
		       has_function_privilege('audit_writer', 'audit.run_partition_maintenance()', 'EXECUTE')
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'audit' AND p.proname = 'run_partition_maintenance'
	`).Scan(&owner, &securityDefiner, &settings, &writerCanExecute); err != nil {
		t.Fatalf("read maintenance function settings: %v", err)
	}
	if owner != fixture.migrationOwner || !securityDefiner || !writerCanExecute {
		t.Fatalf("maintenance function owner %q, SECURITY DEFINER %t, audit_writer EXECUTE %t; want %q, true, true",
			owner, securityDefiner, writerCanExecute, fixture.migrationOwner)
	}
	for _, setting := range []string{"search_path=pg_catalog, partman, audit", "TimeZone=UTC"} {
		if !slices.Contains(settings, setting) {
			t.Fatalf("maintenance function settings %q lack %q", settings, setting)
		}
	}
}

// requireUTCWeekNames requires each moment's week to have a child named
// events_p plus the date of its Monday in UTC, bounded from that Monday at
// 00:00 UTC.
func requireUTCWeekNames(t *testing.T, fixture *partmanTestDatabase, moments []time.Time) {
	t.Helper()
	for _, moment := range moments {
		utc := moment.UTC()
		daysSinceMonday := (int(utc.Weekday()) + 6) % 7
		monday := time.Date(utc.Year(), utc.Month(), utc.Day()-daysSinceMonday, 0, 0, 0, 0, time.UTC)
		name := "events_p" + monday.Format("2006_01_02")
		var bound string
		if err := fixture.pool.QueryRow(context.Background(), `
			SELECT pg_get_expr(c.relpartbound, c.oid)
			FROM pg_inherits i
			JOIN pg_class c ON c.oid = i.inhrelid
			JOIN pg_class p ON p.oid = i.inhparent
			JOIN pg_namespace n ON n.oid = p.relnamespace
			WHERE n.nspname = 'audit' AND p.relname = 'events' AND c.relname = $1
		`, name).Scan(&bound); err != nil {
			t.Fatalf("read partition %s for %s: %v", name, utc.Format(time.DateOnly), err)
		}
		if want := "FOR VALUES FROM ('" + monday.Format(time.DateOnly) + " 00:00:00+00')"; !strings.HasPrefix(bound, want) {
			t.Fatalf("partition %s bound %q, want prefix %q", name, bound, want)
		}
	}
}
