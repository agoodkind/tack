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
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

const (
	schemaGuardProbeTable     = "audit.tack_schema_guard_probe"
	schemaGuardProbeStatement = "CREATE TABLE " + schemaGuardProbeTable + " (id int)"
	schemaGuardRefusalState   = "42501"
	schemaGuardRefusalText    = "schema change refused"
)

type schemaGuardResult struct {
	clispec.ResultMarker
	Command     string `json:"command"`
	SessionRole string `json:"session_role"`
	Statement   string `json:"statement"`
	SQLState    string `json:"sqlstate"`
	Message     string `json:"message"`
}

func runSchemaGuardProof(ctx context.Context, cfg *config.Config, sink clispec.ResultSink) error {
	logger := telemetry.L(ctx)
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		logger.ErrorContext(ctx, "schema_guard.pool_failed", slog.String("err", err.Error()))
		return fmt.Errorf("open the database for the schema guard proof: %w", err)
	}
	defer pool.Close()
	// A superuser login distinguishes a guard refusal from a schema permission error.
	sessionRole, err := requireSuperuserLogin(ctx, pool)
	if err != nil {
		return err
	}
	_, probeErr := pool.Exec(ctx, schemaGuardProbeStatement)
	remained, err := removeSchemaGuardProbe(ctx, pool)
	if err != nil {
		return err
	}
	var refused *pgconn.PgError
	switch {
	case probeErr == nil:
		accepted := fmt.Errorf("the schema guard accepted %q as %s", schemaGuardProbeStatement, sessionRole)
		logger.ErrorContext(ctx, "schema_guard.accepted", slog.String("err", accepted.Error()))
		return accepted
	case !errors.As(probeErr, &refused) || refused.Code != schemaGuardRefusalState ||
		!strings.Contains(refused.Message, schemaGuardRefusalText):
		logger.ErrorContext(ctx, "schema_guard.unexpected_error", slog.String("err", probeErr.Error()))
		return fmt.Errorf("%q as %s returned %w, want the schema guard refusal", schemaGuardProbeStatement, sessionRole, probeErr)
	case remained:
		leftover := fmt.Errorf("the schema guard refused %q, but %s remained", schemaGuardProbeStatement, schemaGuardProbeTable)
		logger.ErrorContext(ctx, "schema_guard.probe_remained", slog.String("err", leftover.Error()))
		return leftover
	}
	logger.InfoContext(ctx, "schema_guard.refused",
		slog.String("session_role", sessionRole), slog.String("sqlstate", refused.Code))
	result := schemaGuardResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.audit.prove-schema-guard",
		SessionRole: sessionRole, Statement: schemaGuardProbeStatement,
		SQLState: refused.Code, Message: refused.Message,
	}
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		logger.ErrorContext(ctx, "schema_guard.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the schema guard proof: %w", err)
	}
	return nil
}

func requireSuperuserLogin(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var sessionRole string
	var superuser bool
	if err := pool.QueryRow(ctx,
		`SELECT session_user::text, rolsuper FROM pg_roles WHERE rolname = session_user`).Scan(&sessionRole, &superuser); err != nil {
		slog.ErrorContext(ctx, "schema_guard.login_read_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("read the database login: %w", err)
	}
	if !superuser {
		refused := fmt.Errorf("the schema guard proof needs the engine superuser login, and the login is %s", sessionRole)
		slog.ErrorContext(ctx, "schema_guard.login_not_superuser", slog.String("err", refused.Error()))
		return "", refused
	}
	return sessionRole, nil
}

func removeSchemaGuardProbe(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var present bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schemaGuardProbeTable).Scan(&present); err != nil {
		slog.ErrorContext(ctx, "schema_guard.probe_read_failed", slog.String("err", err.Error()))
		return false, fmt.Errorf("check for %s: %w", schemaGuardProbeTable, err)
	}
	if !present {
		return false, nil
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "schema_guard.acquire_failed", slog.String("err", err.Error()))
		return true, fmt.Errorf("acquire a connection to drop %s: %w", schemaGuardProbeTable, err)
	}
	defer connection.Release()
	for _, statement := range []string{
		"SET ROLE " + postgres.MigratorRole,
		"DROP TABLE IF EXISTS " + schemaGuardProbeTable,
		"RESET ROLE",
	} {
		if _, err := connection.Exec(ctx, statement); err != nil {
			slog.ErrorContext(ctx, "schema_guard.probe_drop_failed",
				slog.String("statement", statement), slog.String("err", err.Error()))
			return true, fmt.Errorf("drop %s with %q: %w", schemaGuardProbeTable, statement, err)
		}
	}
	return true, nil
}
