//go:build integration

package audit

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"goodkind.io/tack/migrations"
)

func (fixture *partmanTestDatabase) migrateTo(t *testing.T, version int64) {
	t.Helper()
	if err := fixture.migrate(version); err != nil {
		t.Fatalf("migrate to %d: %v", version, err)
	}
}

func (fixture *partmanTestDatabase) migrate(version int64) error {
	fixture.pool.Close()
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	database, err := goose.OpenDBWithDriver("pgx", fixture.dsn)
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	migrationErr := goose.UpToContext(context.Background(), database, ".", version)
	_ = database.Close()
	reopenErr := fixture.reopenPool()
	if migrationErr != nil {
		return fmt.Errorf("goose up to %d: %w", version, migrationErr)
	}
	if reopenErr != nil {
		return reopenErr
	}
	return nil
}

func (fixture *partmanTestDatabase) rollbackTo(t *testing.T, version int64) {
	t.Helper()
	fixture.pool.Close()
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	database, err := goose.OpenDBWithDriver("pgx", fixture.dsn)
	if err != nil {
		t.Fatalf("open rollback database: %v", err)
	}
	rollbackErr := goose.DownToContext(context.Background(), database, ".", version)
	_ = database.Close()
	if err := fixture.reopenPool(); err != nil {
		t.Fatalf("reopen pool after rollback: %v", err)
	}
	if rollbackErr != nil {
		t.Fatalf("goose down to %d: %v", version, rollbackErr)
	}
}

func (fixture *partmanTestDatabase) reopenPool() error {
	config, err := pgxpool.ParseConfig(fixture.dsn)
	if err != nil {
		return fmt.Errorf("parse fixture DSN: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return fmt.Errorf("reopen fixture pool: %w", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return fmt.Errorf("ping reopened fixture pool: %w", err)
	}
	fixture.pool = pool
	return nil
}
