// Package migration applies goose migrations from a file system to
// YugabyteDB. It can apply all pending migrations or stop at a target
// version.
package migration

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// MigratorRole identifies the tack_migrator role that migration 018 creates.
// The role owns the audit, partman, and public schemas and is not a
// superuser.
const MigratorRole = "tack_migrator"

// lastSuperuserMigration identifies migration 019, which creates event
// triggers. Only a superuser can create those triggers (TACK-556).
const lastSuperuserMigration = 19

// Migrate runs all pending goose migrations.
//
// The migrate subcommand, ops provision, and ops ledger audit-bootstrap use
// this migration path. HTTP startup must not run migrations because servers
// scale horizontally.
//
// The migration role depends on the version:
//
//   - Migrations through lastSuperuserMigration use the login role.
//   - A superuser login applies later migrations as tack_migrator. Migration
//     019 refuses changes to the audit and partman schemas by other roles.
func Migrate(ctx context.Context, dsn string, migrationsFS fs.FS) error {
	return MigrateTo(ctx, dsn, migrationsFS, goose.MaxVersion)
}

// MigrateTo runs pending goose migrations through target.
//
// It changes the migration role only when the login is a superuser and
// target exceeds lastSuperuserMigration.
//
//   - Migrations through lastSuperuserMigration use the login role.
//   - Later migrations use tack_migrator when the login is a superuser.
func MigrateTo(ctx context.Context, dsn string, migrationsFS fs.FS, target int64) error {
	connConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"postgres.migrate.dsn_failed",
			slog.Int64("target_version", target),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("parse the migration database address for version %d: %w", target, err)
	}

	if err := migrateTo(
		ctx,
		connConfig,
		migrationsFS,
		min(target, lastSuperuserMigration),
	); err != nil {
		return err
	}

	if target <= lastSuperuserMigration {
		return nil
	}

	superuser, err := loginIsSuperuser(ctx, connConfig)
	if err != nil {
		return err
	}

	if superuser {
		connConfig.RuntimeParams["role"] = MigratorRole
	}

	return migrateTo(ctx, connConfig, migrationsFS, target)
}

func migrateTo(
	ctx context.Context,
	connConfig *pgx.ConnConfig,
	migrationsFS fs.FS,
	version int64,
) error {
	database := stdlib.OpenDB(*connConfig)
	defer func() { _ = database.Close() }()
	provider, err := migrationProvider(ctx, database, migrationsFS)
	if err != nil {
		return err
	}

	if _, err := provider.UpTo(ctx, version); err != nil {
		slog.ErrorContext(
			ctx,
			"postgres.migrate.apply_failed",
			slog.Int64("through_version", version),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("apply migrations through %d: %w", version, err)
	}

	return nil
}

func migrationProvider(
	ctx context.Context,
	database *sql.DB,
	migrationsFS fs.FS,
) (*goose.Provider, error) {
	provider, err := goose.NewProvider(goose.DialectPostgres, database, migrationsFS)
	if err != nil {
		slog.ErrorContext(ctx, "postgres.migrate.load_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("load migrations: %w", err)
	}

	return provider, nil
}

func loginIsSuperuser(ctx context.Context, connConfig *pgx.ConnConfig) (bool, error) {
	connection, err := pgx.ConnectConfig(ctx, connConfig)
	if err != nil {
		slog.ErrorContext(ctx, "postgres.migrate.connect_failed", slog.String("err", err.Error()))
		return false, fmt.Errorf("connect to read the migration login: %w", err)
	}

	defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
	var superuser bool
	if err := connection.QueryRow(
		ctx,
		`SELECT rolsuper FROM pg_roles WHERE rolname = session_user`,
	).Scan(
		&superuser,
	); err != nil {
		slog.ErrorContext(
			ctx,
			"postgres.migrate.login_read_failed",
			slog.String("err", err.Error()),
		)

		return false, fmt.Errorf("read the migration login: %w", err)
	}

	return superuser, nil
}
