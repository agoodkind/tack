package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const (
	// lastSuperuserMigration creates event triggers, which only a superuser
	// creates (TACK-556).
	lastSuperuserMigration = 19
	migratorRole           = "tack_migrator"
	allMigrations          = 0
)

// Migrate runs pending goose migrations. Called by the `migrate` subcommand only,
// never on HTTP server startup (required for safe horizontal scaling).
//
// A superuser login applies the migrations after lastSuperuserMigration under
// the role tack_migrator: the schema guard of migration 019 refuses a change
// to the audit schema by any other role.
func Migrate(ctx context.Context, dsn string, migrationsFS fs.FS) error {
	connConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		slog.ErrorContext(ctx, "postgres.migrate.dsn_failed", slog.String("err", err.Error()))
		return fmt.Errorf("parse the migration database address: %w", err)
	}
	if err := migrateThrough(ctx, connConfig, migrationsFS, lastSuperuserMigration); err != nil {
		return err
	}
	superuser, err := loginIsSuperuser(ctx, connConfig)
	if err != nil {
		return err
	}
	if superuser {
		connConfig.RuntimeParams["role"] = migratorRole
	}
	return migrateThrough(ctx, connConfig, migrationsFS, allMigrations)
}

func migrateThrough(ctx context.Context, connConfig *pgx.ConnConfig, migrationsFS fs.FS, version int64) error {
	database := stdlib.OpenDB(*connConfig)
	defer func() { _ = database.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, database, migrationsFS)
	if err != nil {
		slog.ErrorContext(ctx, "postgres.migrate.load_failed", slog.String("err", err.Error()))
		return fmt.Errorf("load migrations: %w", err)
	}
	if version == allMigrations {
		_, err = provider.Up(ctx)
	} else {
		_, err = provider.UpTo(ctx, version)
	}
	if err != nil {
		slog.ErrorContext(ctx, "postgres.migrate.apply_failed",
			slog.Int64("through_version", version), slog.String("err", err.Error()))
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

func loginIsSuperuser(ctx context.Context, connConfig *pgx.ConnConfig) (bool, error) {
	connection, err := pgx.ConnectConfig(ctx, connConfig)
	if err != nil {
		slog.ErrorContext(ctx, "postgres.migrate.connect_failed", slog.String("err", err.Error()))
		return false, fmt.Errorf("connect to read the migration login: %w", err)
	}
	defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
	var superuser bool
	if err := connection.QueryRow(ctx,
		`SELECT rolsuper FROM pg_roles WHERE rolname = session_user`).Scan(&superuser); err != nil {
		slog.ErrorContext(ctx, "postgres.migrate.login_read_failed", slog.String("err", err.Error()))
		return false, fmt.Errorf("read the migration login: %w", err)
	}
	return superuser, nil
}
