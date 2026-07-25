//go:build integration

package audit

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const partmanTestDSN = "AUDIT_CHAIN_TEST_DSN"

type partmanTestDatabase struct {
	adminPool      *pgxpool.Pool
	pool           *pgxpool.Pool
	databaseName   string
	dsn            string
	migrationOwner string
}

func newPartmanTestDatabase(t *testing.T) *partmanTestDatabase {
	t.Helper()
	baseDSN := os.Getenv(partmanTestDSN)
	if baseDSN == "" {
		t.Skipf("set %s to run the partition adoption tests", partmanTestDSN)
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping admin pool: %v", err)
	}

	databaseName := "tack_partman_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	createDatabaseSQL := fmt.Sprintf(
		"CREATE DATABASE %s TEMPLATE template0",
		pgx.Identifier{databaseName}.Sanitize(),
	)
	if _, err := adminPool.Exec(ctx, createDatabaseSQL); err != nil {
		adminPool.Close()
		t.Fatalf("create test database %s: %v", databaseName, err)
	}

	databaseDSN, err := partmanDatabaseDSN(baseDSN, databaseName)
	if err != nil {
		t.Fatalf("build test database DSN: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseDSN)
	if err != nil {
		t.Fatalf("parse test DSN: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open test pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test database: %v", err)
	}

	fixture := &partmanTestDatabase{
		adminPool:    adminPool,
		pool:         pool,
		databaseName: databaseName,
		dsn:          databaseDSN,
	}
	var connectedDatabase string
	if err := pool.QueryRow(
		ctx,
		`SELECT current_database(), current_user`,
	).Scan(&connectedDatabase, &fixture.migrationOwner); err != nil {
		t.Fatalf("read fixture connection identity: %v", err)
	}
	if connectedDatabase != databaseName {
		t.Fatalf("fixture connected to %q, want %q", connectedDatabase, databaseName)
	}
	t.Cleanup(func() {
		fixture.pool.Close()
		dropDatabaseSQL := fmt.Sprintf(
			"DROP DATABASE %s",
			pgx.Identifier{databaseName}.Sanitize(),
		)
		if _, err := adminPool.Exec(context.Background(), dropDatabaseSQL); err != nil {
			t.Errorf("drop test database %s: %v", databaseName, err)
		}
		adminPool.Close()
	})
	return fixture
}

func partmanDatabaseDSN(baseDSN string, databaseName string) (string, error) {
	parsedDSN, err := url.Parse(baseDSN)
	if err != nil {
		return "", fmt.Errorf("parse base DSN: %w", err)
	}
	if parsedDSN.Scheme != "postgres" && parsedDSN.Scheme != "postgresql" {
		return "", fmt.Errorf("base DSN must use postgres or postgresql scheme")
	}
	parsedDSN.Path = "/" + databaseName
	parsedDSN.RawPath = ""
	return parsedDSN.String(), nil
}

func (fixture *partmanTestDatabase) partitionCount(t *testing.T, pattern string) int {
	t.Helper()
	var count int
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace parent_ns ON parent_ns.oid = p.relnamespace
		JOIN pg_namespace child_ns ON child_ns.oid = c.relnamespace
		WHERE parent_ns.nspname = 'audit'
		  AND p.relname = 'events'
		  AND child_ns.nspname = 'audit'
		  AND c.relname ~ $1
	`, pattern).Scan(&count); err != nil {
		t.Fatalf("partition count for %q: %v", pattern, err)
	}
	return count
}

func (fixture *partmanTestDatabase) insertAuditEvent(
	t *testing.T,
	eventID uuid.UUID,
	eventTime time.Time,
) {
	t.Helper()
	identifier := uuid.Must(uuid.NewV7())
	if _, err := fixture.pool.Exec(context.Background(), `
		INSERT INTO audit.events (
			org_id, shard, event_time, event_id, seq, actor_id, actor_kind,
			action, entity_kind, entity_id, context, delta, pii_ref,
			prev_hash, row_hash, idempotency_key
		) VALUES (
			$1, 0, $2, $3, 1, $4, 0,
			'test.partition_adoption', 'node', $5, '{}', 'null', NULL,
			''::bytea, '\x00'::bytea, $6
		)
	`, identifier, eventTime, eventID, identifier, identifier, uuid.NewString()); err != nil {
		t.Fatalf("insert preserved audit event: %v", err)
	}
}
