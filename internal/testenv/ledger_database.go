package testenv

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the pgx database/sql driver goose migrates through
	"github.com/pressly/goose/v3"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/migrations"
)

const (
	// testDatabasePrefix names every database this package creates. The Unix
	// second of creation follows it, so a later process can tell an abandoned
	// database by its age.
	testDatabasePrefix = "tack_test_"
	// staleDatabaseAge is how old an unused test database must be before a
	// later process drops it. A test binary never runs this long.
	staleDatabaseAge = 3 * time.Hour
	// ledgerLockName is the file lock that serializes database creation,
	// migration, and ChangeRoles among the test binaries of one machine. Roles
	// belong to the whole engine, so concurrent role changes and migrations in
	// different databases conflict.
	ledgerLockName = "tack-testenv-ledger.lock"
)

// createLedgerDatabase creates this process's database, migrates it, and
// returns its connection string.
func createLedgerDatabase(ctx context.Context, running engine, setupDSN string) (string, error) {
	unlock, err := lockEngine(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()

	admin, err := pgx.Connect(ctx, setupDSN)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.connect_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("connect to the test ledger: %w", err)
	}
	defer func() { _ = admin.Close(context.WithoutCancel(ctx)) }()
	dropStaleDatabases(ctx, admin)

	for attempt := 1; ; attempt++ {
		dsn, err := createMigratedDatabase(ctx, admin, running)
		if err == nil {
			return dsn, nil
		}
		if attempt == migrateAttempts || !hasSQLState(err, serializationFailure) {
			return "", err
		}
		slog.DebugContext(ctx, "testenv.ledger.migrate_retry", slog.Int("attempt", attempt), slog.String("err", err.Error()))
	}
}

// migrateAttempts bounds how often a migration that lost a serialization
// conflict is retried on a fresh database. The engine keeps a failed
// migration's completed DDL, so a retry never reuses the half-migrated one.
// Tests in other binaries run DDL on the same engine while this one migrates,
// and the engine fails a migration statement whose catalog snapshot that DDL
// invalidated with SQLSTATE 40001.
const migrateAttempts = 5

// createMigratedDatabase creates one database and migrates it, dropping it
// again when the migration fails.
func createMigratedDatabase(ctx context.Context, admin *pgx.Conn, running engine) (string, error) {
	suffix, err := randomHex(ctx, 4)
	if err != nil {
		return "", err
	}
	name := testDatabasePrefix + strconv.FormatInt(clock.Now().Unix(), 10) + "_" + suffix
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.create_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("create database %s: %w", name, err)
	}
	dsn := ledgerDSN(running, name)
	if err := migrateLedger(ctx, dsn); err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+identifier)
		slog.ErrorContext(ctx, "testenv.ledger.migrate_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("migrate database %s: %w", name, err)
	}
	slog.InfoContext(ctx, "testenv.ledger.ready", slog.String("database", name))
	return dsn, nil
}

// serializationFailure is the SQLSTATE of a transaction that lost a conflict.
const serializationFailure = "40001"

// migrateLedger applies every embedded migration, the same set
// `./server migrate` applies.
func migrateLedger(ctx context.Context, dsn string) error {
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.open_failed", slog.String("err", err.Error()))
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = database.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, database, migrations.FS)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.migrate_failed", slog.String("err", err.Error()))
		return fmt.Errorf("load migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.migrate_failed", slog.String("err", err.Error()))
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// dropStaleDatabases removes test databases that are old and have no open
// connection, so a reused engine does not collect one database per past run.
// A failure here costs disk, not correctness, so it is logged and ignored.
func dropStaleDatabases(ctx context.Context, admin *pgx.Conn) {
	rows, err := admin.Query(ctx, `
		SELECT datname FROM pg_database
		 WHERE datname LIKE $1
		   AND datname NOT IN (SELECT datname FROM pg_stat_activity WHERE datname IS NOT NULL)`,
		testDatabasePrefix+"%")
	if err != nil {
		slog.InfoContext(ctx, "testenv.ledger.stale_list_failed", slog.String("err", err.Error()))
		return
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		slog.InfoContext(ctx, "testenv.ledger.stale_list_failed", slog.String("err", err.Error()))
		return
	}
	for _, name := range names {
		if !isStale(name) {
			continue
		}
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()); err != nil {
			slog.DebugContext(ctx, "testenv.ledger.stale_drop_failed",
				slog.String("database", name), slog.String("err", err.Error()))
		}
	}
}

// isStale reports whether a test database's name records a creation time
// older than staleDatabaseAge.
func isStale(name string) bool {
	created, _, found := strings.Cut(strings.TrimPrefix(name, testDatabasePrefix), "_")
	if !found {
		return false
	}
	seconds, err := strconv.ParseInt(created, 10, 64)
	if err != nil {
		return false
	}
	return clock.Since(time.Unix(seconds, 0)) > staleDatabaseAge
}
