package postgres

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// New creates a connection pool and runs pending goose migrations.
// migrationsFS must be the embed.FS exported from the migrations package.
func New(ctx context.Context, dsn string, migrationsFS fs.FS) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	if err := migrate(ctx, dsn, migrationsFS); err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	return pool, nil
}

func migrate(ctx context.Context, dsn string, migrationsFS fs.FS) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	db, err := goose.OpenDBWithDriver("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	return goose.UpContext(ctx, db, ".")
}
