//go:build integration

package audit

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPartmanAdoptionRejectsUnparseableChildBounds(t *testing.T) {
	fixture := newPartmanTestDatabase(t)
	fixture.migrateTo(t, 3)
	ctx := context.Background()

	rows, err := fixture.pool.Query(ctx, `
		SELECT child_ns.nspname, c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace parent_ns ON parent_ns.oid = p.relnamespace
		JOIN pg_namespace child_ns ON child_ns.oid = c.relnamespace
		WHERE parent_ns.nspname = 'audit'
		  AND p.relname = 'events'
	`)
	if err != nil {
		t.Fatalf("query existing partitions: %v", err)
	}
	var partitions []pgx.Identifier
	for rows.Next() {
		var schemaName string
		var tableName string
		if err := rows.Scan(&schemaName, &tableName); err != nil {
			t.Fatalf("scan existing partition: %v", err)
		}
		partitions = append(partitions, pgx.Identifier{schemaName, tableName})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate existing partitions: %v", err)
	}
	rows.Close()
	for _, partition := range partitions {
		dropSQL := fmt.Sprintf("DROP TABLE %s", partition.Sanitize())
		if _, err := fixture.pool.Exec(ctx, dropSQL); err != nil {
			t.Fatalf("drop existing partition %s: %v", partition.Sanitize(), err)
		}
	}
	if _, err := fixture.pool.Exec(ctx, `
		CREATE TABLE audit.events_unbounded
		PARTITION OF audit.events DEFAULT
	`); err != nil {
		t.Fatalf("create unparseable default partition: %v", err)
	}

	err = fixture.migrate(4)
	if err == nil {
		t.Fatal("migration with unparseable child bounds succeeded, want error")
	}
	const expected = "cannot determine audit.events partition upper bound"
	if !strings.Contains(err.Error(), expected) {
		t.Fatalf("unparseable bound error = %q, want %q", err, expected)
	}
}
