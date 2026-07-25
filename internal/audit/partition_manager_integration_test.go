//go:build integration

package audit

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPartitionManagerHealsFutureWeek proves boot maintenance restores a
// missing future partition before the audit ledger needs it.
func TestPartitionManagerHealsFutureWeek(t *testing.T) {
	fixture := newPartmanTestDatabase(t)
	fixture.migrateTo(t, 4)
	pool := fixture.pool
	ctx := context.Background()
	target := time.Now().UTC().AddDate(0, 0, 7*20)

	// Extend this isolated fixture until the target is its maintenance frontier.
	if _, err := pool.Exec(ctx, `
		UPDATE partman.part_config
		SET premake = round(
		    EXTRACT(
		        'epoch' FROM age(
		            date_trunc('week', $1::timestamptz),
		            CURRENT_TIMESTAMP
		        )
		    ) / EXTRACT('epoch' FROM partition_interval::interval)
		)::integer
		WHERE parent_table = 'audit.events'
	`, target); err != nil {
		t.Fatalf("extend fixture partition horizon: %v", err)
	}
	if _, err := pool.Exec(ctx, `SELECT audit.run_partition_maintenance()`); err != nil {
		t.Fatalf("seed target partition: %v", err)
	}
	if covering := partitionCountCovering(t, pool, target); covering != 1 {
		t.Fatalf(
			"partitions covering %s before drop = %d, want 1",
			target.Format(time.DateOnly),
			covering,
		)
	}
	dropPartitionsCovering(t, pool, target)
	if covering := partitionCountCovering(t, pool, target); covering != 0 {
		t.Fatalf(
			"partitions covering %s after drop = %d, want 0",
			target.Format(time.DateOnly),
			covering,
		)
	}

	orgID := uuid.Must(uuid.NewV7())
	err := insertPartitionManagerTestEvent(ctx, pool, orgID, target)
	if err == nil {
		t.Fatalf("insert at %s before manager start succeeded, want error", target.Format(time.DateOnly))
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23514" {
		t.Fatalf("insert at %s before manager start error = %v, want SQLSTATE 23514",
			target.Format(time.DateOnly), err)
	}
	t.Logf("pre-start insert failed as expected: %v", err)

	manager := NewPartitionManager(NewPGPartitionStore(pool), time.Hour)
	manager.Start(ctx)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close partition manager: %v", err)
		}
	})

	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := insertPartitionManagerTestEvent(ctx, pool, orgID, target)
		if err == nil {
			t.Logf("post-start insert succeeded at %s", target.Format(time.DateOnly))
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("insert at future week still failing after maintenance: %v", err)
		case <-ticker.C:
		}
	}
}

func dropPartitionsCovering(t *testing.T, pool *pgxpool.Pool, target time.Time) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT child_ns.nspname, c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace parent_ns ON parent_ns.oid = p.relnamespace
		JOIN pg_namespace child_ns ON child_ns.oid = c.relnamespace
		WHERE parent_ns.nspname = 'audit'
		  AND p.relname = 'events'
		  AND $1::timestamptz >= (
		      regexp_match(
		          pg_get_expr(c.relpartbound, c.oid),
		          'FROM \(''([0-9 :.+-]+)'''
		      )
		  )[1]::timestamptz
		  AND $1::timestamptz < (
		      regexp_match(
		          pg_get_expr(c.relpartbound, c.oid),
		          'TO \(''([0-9 :.+-]+)'''
		      )
		  )[1]::timestamptz
	`, target)
	if err != nil {
		t.Fatalf("query partitions covering target: %v", err)
	}
	var partitions []pgx.Identifier
	for rows.Next() {
		var schemaName string
		var tableName string
		if err := rows.Scan(&schemaName, &tableName); err != nil {
			rows.Close()
			t.Fatalf("scan partition covering target: %v", err)
		}
		partitions = append(partitions, pgx.Identifier{schemaName, tableName})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate partitions covering target: %v", err)
	}
	rows.Close()
	for _, partition := range partitions {
		dropSQL := fmt.Sprintf("DROP TABLE %s", partition.Sanitize())
		if _, err := pool.Exec(context.Background(), dropSQL); err != nil {
			t.Fatalf("drop target partition %s: %v", partition.Sanitize(), err)
		}
		t.Logf("dropped target partition %s", partition.Sanitize())
	}
}

func partitionCountCovering(t *testing.T, pool *pgxpool.Pool, target time.Time) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = 'audit'
		  AND p.relname = 'events'
		  AND $1::timestamptz >= (
		      regexp_match(
		          pg_get_expr(c.relpartbound, c.oid),
		          'FROM \(''([0-9 :.+-]+)'''
		      )
		  )[1]::timestamptz
		  AND $1::timestamptz < (
		      regexp_match(
		          pg_get_expr(c.relpartbound, c.oid),
		          'TO \(''([0-9 :.+-]+)'''
		      )
		  )[1]::timestamptz
	`, target).Scan(&count); err != nil {
		t.Fatalf("count partitions covering target: %v", err)
	}
	return count
}

func insertPartitionManagerTestEvent(
	ctx context.Context,
	pool *pgxpool.Pool,
	orgID uuid.UUID,
	eventTime time.Time,
) error {
	identifier := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit.events (
			org_id, shard, event_time, event_id, seq, actor_id, actor_kind,
			action, entity_kind, entity_id, context, delta, pii_ref,
			prev_hash, row_hash, idempotency_key
		) VALUES (
			$1, 0, $2, $3, 1, $4, 0,
			'test.partition_manager', 'node', $5, '{}', 'null', NULL,
			''::bytea, '\x00'::bytea, $6
		)
	`, orgID, eventTime, identifier, identifier, identifier, uuid.NewString()); err != nil {
		return fmt.Errorf("insert partition-manager test event: %w", err)
	}
	return nil
}
