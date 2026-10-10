package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
)

func requireMigratorMembership(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var sessionRole string
	var member bool
	if err := pool.QueryRow(ctx,
		`SELECT session_user::text, pg_has_role(session_user, $1::name, 'MEMBER')`,
		postgres.MigratorRole).Scan(&sessionRole, &member); err != nil {
		slog.ErrorContext(ctx, "schema_guard.login_read_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("read the database login: %w", err)
	}
	if !member {
		refused := fmt.Errorf("membership in role %s is required for login %s", postgres.MigratorRole, sessionRole)
		slog.ErrorContext(ctx, "schema_guard.login_not_member", slog.String("err", refused.Error()))
		return "", refused
	}
	return sessionRole, nil
}

func attemptSchemaGuardProbe(ctx context.Context, pool *pgxpool.Pool) (*pgconn.PgError, error) {
	connection, err := pool.Acquire(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "schema_guard.acquire_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("acquire a connection for the schema guard probe: %w", err)
	}
	defer connection.Release()
	defer func() {
		if _, err := connection.Exec(context.WithoutCancel(ctx), "RESET ROLE"); err != nil {
			slog.ErrorContext(ctx, "schema_guard.role_reset_failed", slog.String("err", err.Error()))
		}
	}()
	for _, statement := range []string{
		"SET ROLE " + postgres.MigratorRole,
		"CREATE ROLE " + schemaGuardProbeRole + " NOLOGIN",
		"GRANT " + schemaGuardProbeRole + " TO " + postgres.MigratorRole,
		"GRANT USAGE, CREATE ON SCHEMA audit TO " + schemaGuardProbeRole,
		"SET ROLE " + schemaGuardProbeRole,
	} {
		if _, err := connection.Exec(ctx, statement); err != nil {
			slog.ErrorContext(ctx, "schema_guard.probe_setup_failed",
				slog.String("statement", statement), slog.String("err", err.Error()))
			return nil, fmt.Errorf("prepare the schema guard probe with %q: %w", statement, err)
		}
	}
	_, probeErr := connection.Exec(ctx, schemaGuardProbeStatement)
	if probeErr == nil {
		accepted := fmt.Errorf("the schema guard accepted %q as %s", schemaGuardProbeStatement, schemaGuardProbeRole)
		slog.ErrorContext(ctx, "schema_guard.accepted", slog.String("err", accepted.Error()))
		return nil, accepted
	}
	var refused *pgconn.PgError
	if !errors.As(probeErr, &refused) || refused.Code != schemaGuardRefusalState ||
		!strings.Contains(refused.Message, schemaGuardRefusalText) {
		slog.ErrorContext(ctx, "schema_guard.unexpected_error", slog.String("err", probeErr.Error()))
		return nil, fmt.Errorf("%q as %s returned %w, want the schema guard refusal",
			schemaGuardProbeStatement, schemaGuardProbeRole, probeErr)
	}
	return refused, nil
}

func removeSchemaGuardProbe(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	ctx = context.WithoutCancel(ctx)
	var tablePresent, rolePresent bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass($1) IS NOT NULL, EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $2)`,
		schemaGuardProbeTable, schemaGuardProbeRole).Scan(&tablePresent, &rolePresent); err != nil {
		slog.ErrorContext(ctx, "schema_guard.probe_read_failed", slog.String("err", err.Error()))
		return false, fmt.Errorf("check for %s and role %s: %w", schemaGuardProbeTable, schemaGuardProbeRole, err)
	}
	if !tablePresent && !rolePresent {
		return false, nil
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "schema_guard.acquire_failed", slog.String("err", err.Error()))
		return tablePresent, fmt.Errorf("acquire a connection to remove the schema guard probe: %w", err)
	}
	defer connection.Release()
	statements := []string{"SET ROLE " + postgres.MigratorRole, "DROP TABLE IF EXISTS " + schemaGuardProbeTable}
	if rolePresent {
		statements = append(statements,
			"REVOKE ALL ON SCHEMA audit FROM "+schemaGuardProbeRole, "DROP ROLE "+schemaGuardProbeRole)
	}
	statements = append(statements, "RESET ROLE")
	for _, statement := range statements {
		if _, err := connection.Exec(ctx, statement); err != nil {
			slog.ErrorContext(ctx, "schema_guard.probe_drop_failed",
				slog.String("statement", statement), slog.String("err", err.Error()))
			return tablePresent, fmt.Errorf("remove the schema guard probe with %q: %w", statement, err)
		}
	}
	return tablePresent, nil
}
