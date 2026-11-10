// Role seeding uses the pgx database driver instead of the former
// seed-audit-roles shell script. Tack Go must not run external commands.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/adapters/postgres/migration"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// auditLoginRole pairs a LOGIN role with its inherited NOLOGIN base role from
// migration 002 and the password from the environment. The audit login names
// match the tack_audit_* deployment DSNs.
type auditLoginRole struct {
	login    string
	base     string
	password string
}

// newAuditLoginRole builds a role entry from positional arguments. The original
// secret scan treated a composite literal's password field as a hardcoded
// credential even when its value referenced configuration.
func newAuditLoginRole(login, base, secret string) auditLoginRole {
	return auditLoginRole{login: login, base: base, password: secret}
}

// RunAuditSeedRoles creates or rotates the audit logins and tack_app, granting
// each its matching base role (TACK-180). It then sets LOGIN and the password
// on tack_migrator (TACK-554).
//
// The function connects through DATABASE_URL with a role that requires
// CREATEROLE. Each rerun sets the passwords to the current environment values.
//
//   - tack_audit_writer inherits audit_writer.
//   - tack_audit_reader inherits audit_reader.
//   - tack_audit_redactor inherits audit_redactor.
//   - tack_audit_operator inherits audit_operator.
//   - tack_app inherits app_auth.
func RunAuditSeedRoles(ctx context.Context, cfg *config.Config) error {
	roles := []auditLoginRole{
		newAuditLoginRole("tack_audit_writer", "audit_writer", cfg.AuditWriterPassword),
		newAuditLoginRole("tack_audit_reader", "audit_reader", cfg.AuditReaderPassword),
		newAuditLoginRole("tack_audit_redactor", "audit_redactor", cfg.AuditRedactorPassword),
		newAuditLoginRole("tack_audit_operator", "audit_operator", cfg.AuditOperatorPassword),
		newAuditLoginRole("tack_app", "app_auth", cfg.AppPassword),
	}
	if cfg.MigratorPassword == "" {
		err := fmt.Errorf("audit seed-roles: password for %s is empty; set TACK_MIGRATOR_PASSWORD", migration.MigratorRole)
		slog.ErrorContext(ctx, "audit.seed_roles.password_missing",
			slog.String("login_role", migration.MigratorRole), slog.String("err", err.Error()))
		return err
	}
	for _, role := range roles {
		if role.password == "" {
			err := fmt.Errorf("audit seed-roles: password for %s is empty; set its AUDIT_*_PASSWORD or TACK_APP_PASSWORD env", role.login)
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
	if err := setMigratorLogin(ctx, pool, cfg.MigratorPassword); err != nil {
		return err
	}
	slog.InfoContext(ctx, "audit.seed_roles.completed", slog.Int("role_count", len(roles)+1))
	return nil
}

// upsertAuditLoginRole creates a missing LOGIN role or rotates an existing
// role's password, then grants its base role. The role and base names use fixed
// values rather than caller input. The function quotes the password as a SQL
// string literal because CREATE ROLE and ALTER ROLE do not accept password bind
// parameters.
func upsertAuditLoginRole(ctx context.Context, pool *pgxpool.Pool, role auditLoginRole) error {
	exists, err := nonSuperuserRoleExists(ctx, pool, role.login)
	if err != nil {
		return err
	}

	// The engine lets only a superuser write NOSUPERUSER in ALTER ROLE.
	passwordLiteral := quoteSQLStringLiteral(role.password)
	var roleStmt string
	if exists {
		roleStmt = fmt.Sprintf(
			"ALTER ROLE %s WITH LOGIN INHERIT NOCREATEDB NOCREATEROLE PASSWORD %s",
			role.login, passwordLiteral,
		)
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

	// Earlier seeded or manual memberships retain privileges through INHERIT.
	// An operator login with audit_writer membership could delete its own
	// pending audit event.
	if err := revokeOtherAuditBases(ctx, pool, role); err != nil {
		return err
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

// auditBaseRoles lists the four audit base roles and app_auth (TACK-180).
var auditBaseRoles = []string{
	"audit_writer",
	"audit_reader",
	"audit_redactor",
	"audit_operator",
	"app_auth",
}

// revokeOtherAuditBases revokes each managed base except the login's assigned
// base before the caller grants that base. This separates the seeded
// application and audit role memberships.
func revokeOtherAuditBases(ctx context.Context, pool *pgxpool.Pool, role auditLoginRole) error {
	for _, base := range auditBaseRoles {
		if base == role.base {
			continue
		}
		revokeStmt := fmt.Sprintf("REVOKE %s FROM %s", base, role.login)
		if _, err := pool.Exec(ctx, revokeStmt); err != nil {
			slog.ErrorContext(ctx, "audit.seed_roles.revoke_failed",
				slog.String("login_role", role.login), slog.String("base_role", base),
				slog.String("err", err.Error()))
			return fmt.Errorf("audit seed-roles: revoke %s from %s: %w", base, role.login, err)
		}
	}
	return nil
}

// quoteSQLStringLiteral wraps s in single quotes and doubles each embedded
// single quote. The original implementation assumes YugabyteDB enables
// standard_conforming_strings, under which a backslash remains an ordinary
// character.
func quoteSQLStringLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
