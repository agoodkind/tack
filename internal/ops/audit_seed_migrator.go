package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
)

// The engine superuser runs this ALTER ROLE on a first boot, and tack_migrator
// runs it on itself afterward. The statement must not remove CREATEROLE.
func setMigratorLogin(ctx context.Context, pool *pgxpool.Pool, secret string) error {
	exists, err := nonSuperuserRoleExists(ctx, pool, postgres.MigratorRole)
	if err != nil {
		return err
	}
	if !exists {
		missing := fmt.Errorf("audit seed-roles: role %s does not exist; run the migrations first", postgres.MigratorRole)
		slog.ErrorContext(ctx, "audit.seed_roles.migrator_missing", slog.String("err", missing.Error()))
		return missing
	}
	statement := fmt.Sprintf("ALTER ROLE %s WITH LOGIN PASSWORD %s", postgres.MigratorRole, quoteSQLStringLiteral(secret))
	if _, err := pool.Exec(ctx, statement); err != nil {
		slog.ErrorContext(ctx, "audit.seed_roles.upsert_failed",
			slog.String("login_role", postgres.MigratorRole), slog.String("err", err.Error()))
		return fmt.Errorf("audit seed-roles: set the login of %s: %w", postgres.MigratorRole, err)
	}
	return nil
}

// nonSuperuserRoleExists returns an error for a role that is a superuser.
func nonSuperuserRoleExists(ctx context.Context, pool *pgxpool.Pool, login string) (bool, error) {
	var superuser bool
	err := pool.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = $1`, login).Scan(&superuser)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "audit.seed_roles.check_failed",
			slog.String("login_role", login), slog.String("err", err.Error()))
		return false, fmt.Errorf("audit seed-roles: check %s: %w", login, err)
	}
	if superuser {
		refusal := fmt.Errorf("audit seed-roles: role %s is a superuser; seed-roles does not change it", login)
		slog.ErrorContext(ctx, "audit.seed_roles.superuser_refused",
			slog.String("login_role", login), slog.String("err", refusal.Error()))
		return false, refusal
	}
	return true, nil
}
