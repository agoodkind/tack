package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migratorLogin is the role that migration 018 creates. It owns the audit,
// partman, and public objects, and `./server migrate` and the operator
// commands connect as it (TACK-554).
const migratorLogin = "tack_migrator"

// setMigratorLogin sets LOGIN and the password on tack_migrator. Migration 018
// creates the role without a login and with CREATEROLE. The statement names
// no other attribute, and the role runs it on itself after the deployment
// stops connecting as the engine superuser.
func setMigratorLogin(ctx context.Context, pool *pgxpool.Pool, secret string) error {
	exists, err := loginRoleExists(ctx, pool, migratorLogin)
	if err != nil {
		return err
	}
	if !exists {
		missing := fmt.Errorf("audit seed-roles: role %s does not exist; run the migrations first", migratorLogin)
		slog.ErrorContext(ctx, "audit.seed_roles.migrator_missing", slog.String("err", missing.Error()))
		return missing
	}
	statement := fmt.Sprintf("ALTER ROLE %s WITH LOGIN PASSWORD %s", migratorLogin, quoteSQLStringLiteral(secret))
	if _, err := pool.Exec(ctx, statement); err != nil {
		slog.ErrorContext(ctx, "audit.seed_roles.upsert_failed",
			slog.String("login_role", migratorLogin), slog.String("err", err.Error()))
		return fmt.Errorf("audit seed-roles: set the login of %s: %w", migratorLogin, err)
	}
	return nil
}

// loginRoleExists reports whether the role exists, and refuses a role that is
// a superuser: seed-roles gives no login the superuser attribute and rotates
// no password of one.
func loginRoleExists(ctx context.Context, pool *pgxpool.Pool, login string) (bool, error) {
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
