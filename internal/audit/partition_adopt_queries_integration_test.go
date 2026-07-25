//go:build integration

package audit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func (fixture *partmanTestDatabase) futurePartitionCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = 'audit'
		  AND p.relname = 'events'
		  AND (regexp_match(
		      pg_get_expr(c.relpartbound, c.oid),
		      'FROM \(''([0-9 :.+-]+)'''
		  ))[1]::timestamptz > now()
	`).Scan(&count); err != nil {
		t.Fatalf("future partition count: %v", err)
	}
	return count
}

func (fixture *partmanTestDatabase) dropLatestEmptyFuturePartition(t *testing.T) {
	t.Helper()
	var schemaName string
	var tableName string
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT child_ns.nspname, c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace parent_ns ON parent_ns.oid = p.relnamespace
		JOIN pg_namespace child_ns ON child_ns.oid = c.relnamespace
		WHERE parent_ns.nspname = 'audit'
		  AND p.relname = 'events'
		  AND (regexp_match(
		      pg_get_expr(c.relpartbound, c.oid),
		      'FROM \(''([0-9 :.+-]+)'''
		  ))[1]::timestamptz > now()
		ORDER BY (
		    regexp_match(
		        pg_get_expr(c.relpartbound, c.oid),
		        'TO \(''([0-9 :.+-]+)'''
		    )
		)[1]::timestamptz DESC
		LIMIT 1
	`).Scan(&schemaName, &tableName); err != nil {
		t.Fatalf("find latest future partition: %v", err)
	}
	var rows int
	countSQL := fmt.Sprintf(
		"SELECT count(*) FROM %s",
		pgx.Identifier{schemaName, tableName}.Sanitize(),
	)
	if err := fixture.pool.QueryRow(context.Background(), countSQL).Scan(&rows); err != nil {
		t.Fatalf("count latest future partition rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("latest future partition rows = %d, want 0", rows)
	}
	dropSQL := fmt.Sprintf(
		"DROP TABLE %s",
		pgx.Identifier{schemaName, tableName}.Sanitize(),
	)
	if _, err := fixture.pool.Exec(context.Background(), dropSQL); err != nil {
		t.Fatalf("drop latest empty future partition: %v", err)
	}
}

func (fixture *partmanTestDatabase) assertPartitionCovers(
	t *testing.T,
	target time.Time,
) {
	t.Helper()
	var count int
	if err := fixture.pool.QueryRow(context.Background(), `
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
		t.Fatalf("covering partition query: %v", err)
	}
	if count != 1 {
		t.Fatalf("partitions covering %s = %d, want 1", target.Format(time.DateOnly), count)
	}
}
