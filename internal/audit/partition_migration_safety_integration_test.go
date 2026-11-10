//go:build integration

package audit

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPartmanAdoptionStartsAfterLatestTimestampBound(t *testing.T) {
	fixture := newPartmanTestDatabase(t)
	fixture.migrateTo(t, 3)
	ctx := context.Background()

	var sourceName string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT 'events_' || to_char(
		           date_trunc('week', now()) + interval '9 weeks',
		           'YYYY_MM_DD'
		       )
	`).Scan(&sourceName); err != nil {
		t.Fatalf("future legacy partition identity: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		SELECT audit.ensure_events_partition(
		    (date_trunc('week', now()) + interval '9 weeks')::date
		)
	`); err != nil {
		t.Fatalf("create future legacy partition: %v", err)
	}
	renameSQL := fmt.Sprintf(
		"ALTER TABLE %s RENAME TO events_extended",
		pgx.Identifier{"audit", sourceName}.Sanitize(),
	)
	if _, err := fixture.pool.Exec(ctx, renameSQL); err != nil {
		t.Fatalf("rename future legacy partition: %v", err)
	}

	fixture.migrateTo(t, 4)
	var registered bool
	if err := fixture.pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1
		    FROM partman.part_config
		    WHERE parent_table = 'audit.events'
		)
	`).Scan(&registered); err != nil {
		t.Fatalf("partman registration query: %v", err)
	}
	if !registered {
		t.Fatal("audit.events is not registered after adoption")
	}
}

func TestPartmanAdoptionRejectsDetachedNameCollisionBeforeRenaming(t *testing.T) {
	fixture := newPartmanTestDatabase(t)
	fixture.migrateTo(t, 3)
	ctx := context.Background()

	var sourceName string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = 'audit'
		  AND p.relname = 'events'
		  AND c.relname ~ '^events_[0-9]{4}_[0-9]{2}_[0-9]{2}$'
		ORDER BY c.relname
		LIMIT 1
	`).Scan(&sourceName); err != nil {
		t.Fatalf("find hand-named partition: %v", err)
	}
	targetName := strings.Replace(sourceName, "events_", "events_p", 1)
	createDetachedSQL := fmt.Sprintf(
		"CREATE TABLE %s (LIKE audit.events)",
		pgx.Identifier{"audit", targetName}.Sanitize(),
	)
	if _, err := fixture.pool.Exec(ctx, createDetachedSQL); err != nil {
		t.Fatalf("create detached collision: %v", err)
	}

	oldCount := fixture.partitionCount(t, `^events_[0-9]{4}_[0-9]{2}_[0-9]{2}$`)
	err := fixture.migrate(4)
	if err == nil {
		t.Fatal("migration with detached target collision succeeded, want error")
	}
	if !strings.Contains(err.Error(), "detached target relation") {
		t.Fatalf("collision error = %q, want detached target relation", err)
	}
	if got := fixture.partitionCount(
		t,
		`^events_[0-9]{4}_[0-9]{2}_[0-9]{2}$`,
	); got != oldCount {
		t.Fatalf("hand-named partitions after preflight failure = %d, want %d", got, oldCount)
	}
}

func TestPartmanRollbackRestoresHandNamedPartitions(t *testing.T) {
	fixture := newPartmanTestDatabase(t)
	fixture.migrateTo(t, 4)
	fixture.rollbackTo(t, 3)

	if nativeCount := fixture.partitionCount(
		t,
		`^events_p[0-9]{4}_[0-9]{2}_[0-9]{2}$`,
	); nativeCount != 0 {
		t.Fatalf("native partition count after rollback = %d, want 0", nativeCount)
	}
	if oldCount := fixture.partitionCount(
		t,
		`^events_[0-9]{4}_[0-9]{2}_[0-9]{2}$`,
	); oldCount == 0 {
		t.Fatal("hand-named partition count after rollback = 0, want at least 1")
	}

	weekStart := time.Now().UTC().Truncate(24 * time.Hour)
	weekStart = weekStart.AddDate(0, 0, -int(weekStart.Weekday())+1)
	if _, err := fixture.pool.Exec(
		context.Background(),
		`SELECT audit.ensure_events_partition($1::date)`,
		weekStart,
	); err != nil {
		t.Fatalf("legacy partition helper after rollback: %v", err)
	}
}
