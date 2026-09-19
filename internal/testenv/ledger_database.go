package testenv

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
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
	// ledgerLockName is the file lock that serializes database creation and
	// migration among the test binaries of one machine. The migrations create
	// cluster-wide roles, which two concurrent migrations would race on.
	ledgerLockName = "tack-testenv-ledger.lock"
)

// createLedgerDatabase creates this process's database, migrates it, and
// returns its connection string.
func createLedgerDatabase(ctx context.Context, running engine, setupDSN string) (string, error) {
	lock := flock.New(filepath.Join(os.TempDir(), ledgerLockName))
	if err := lock.Lock(); err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.lock_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("lock %s: %w", lock.Path(), err)
	}
	defer func() { _ = lock.Unlock() }()

	admin, err := pgx.Connect(ctx, setupDSN)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.connect_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("connect to the test ledger: %w", err)
	}
	defer func() { _ = admin.Close(context.WithoutCancel(ctx)) }()
	dropStaleDatabases(ctx, admin)

	suffix, err := randomHex(ctx, 4)
	if err != nil {
		return "", err
	}
	name := testDatabasePrefix + strconv.FormatInt(clock.Now().Unix(), 10) + "_" + suffix
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.create_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("create database %s: %w", name, err)
	}
	dsn := ledgerDSN(running, name)
	if err := migrateLedger(ctx, dsn); err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.migrate_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("migrate database %s: %w", name, err)
	}
	slog.InfoContext(ctx, "testenv.ledger.ready", slog.String("database", name))
	return dsn, nil
}

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
