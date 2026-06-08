// audit_seed_roles.go: create or rotate the LOGIN-capable audit roles the
// application DSNs authenticate as, granting each the matching NOLOGIN base
// role from migrations/002_audit.sql. Idempotent: re-running rotates each
// password to the current env value. Replaces the former
// scripts/seed-audit-roles.sh; the op uses pgx, never a shell-out, per the
// binding rule that no tack Go path shells out.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// auditLoginRole pairs a LOGIN role the app DSN connects as with the NOLOGIN
// base role (created by migration 002) it inherits, plus the env password to
// set on it. The login names match the configs DSNs (tack_audit_*).
type auditLoginRole struct {
	login    string
	base     string
	password string
}

// RunAuditSeedRoles creates or rotates the three LOGIN audit roles
// (tack_audit_writer, tack_audit_reader, tack_audit_redactor), each granting
// the matching base role (audit_writer, audit_reader, audit_redactor). It
// connects as DATABASE_URL, which holds role-creation privilege because the
// migrate path creates the base roles through the same DSN. Idempotent.
func RunAuditSeedRoles(ctx context.Context, cfg *config.Config) error {
	roles := []auditLoginRole{
		{login: "tack_audit_writer", base: "audit_writer", password: cfg.AuditWriterPassword},
		{login: "tack_audit_reader", base: "audit_reader", password: cfg.AuditReaderPassword},
		{login: "tack_audit_redactor", base: "audit_redactor", password: cfg.AuditRedactorPassword},
	}
	for _, role := range roles {
		if role.password == "" {
			err := fmt.Errorf("audit seed-roles: password for %s is empty; set its AUDIT_*_PASSWORD env", role.login)
			slog.ErrorContext(ctx, "audit.seed_roles.password_missing",
				slog.String("login_role", role.login), slog.String("err", err.Error()))
			return err
		}
	}

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		slog.ErrorContext(ctx, "audit.seed_roles.pool_open_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit seed-roles: open admin pool: %w", err)
	}
	defer pool.Close()

	for _, role := range roles {
		if err := upsertAuditLoginRole(ctx, pool, role); err != nil {
			return err
		}
	}
	slog.InfoContext(ctx, "audit.seed_roles.completed", slog.Int("role_count", len(roles)))
	return nil
}

// upsertAuditLoginRole creates the LOGIN role when absent or rotates its
// password when present, then grants the base role. The role and base names
// are fixed constants, never caller input, so interpolating them is safe;
// the password is escaped as a SQL string literal because CREATE/ALTER ROLE
// does not accept bind parameters for the password.
func upsertAuditLoginRole(ctx context.Context, pool *pgxpool.Pool, role auditLoginRole) error {
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role.login,
	).Scan(&exists); err != nil {
		slog.ErrorContext(ctx, "audit.seed_roles.check_failed",
			slog.String("login_role", role.login), slog.String("err", err.Error()))
		return fmt.Errorf("audit seed-roles: check %s: %w", role.login, err)
	}

	passwordLiteral := quoteSQLStringLiteral(role.password)
	var roleStmt string
	if exists {
		roleStmt = fmt.Sprintf("ALTER ROLE %s WITH LOGIN PASSWORD %s", role.login, passwordLiteral)
	} else {
		roleStmt = fmt.Sprintf(
			"CREATE ROLE %s LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %s",
			role.login, passwordLiteral,
		)
	}
	if _, err := pool.Exec(ctx, roleStmt); err != nil {
		slog.ErrorContext(ctx, "audit.seed_roles.upsert_failed",
			slog.String("login_role", role.login), slog.String("err", err.Error()))
		return fmt.Errorf("audit seed-roles: upsert %s: %w", role.login, err)
	}

	grantStmt := fmt.Sprintf("GRANT %s TO %s", role.base, role.login)
	if _, err := pool.Exec(ctx, grantStmt); err != nil {
		slog.ErrorContext(ctx, "audit.seed_roles.grant_failed",
			slog.String("login_role", role.login), slog.String("base_role", role.base),
			slog.String("err", err.Error()))
		return fmt.Errorf("audit seed-roles: grant %s to %s: %w", role.base, role.login, err)
	}
	return nil
}

// quoteSQLStringLiteral wraps s in single quotes and doubles any embedded
// single quote. Safe under standard_conforming_strings (the YugabyteDB
// default), where a backslash is an ordinary character.
func quoteSQLStringLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
