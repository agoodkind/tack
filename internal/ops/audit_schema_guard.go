package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

const (
	schemaGuardProbeTable     = "audit.tack_schema_guard_probe"
	schemaGuardProbeRole      = "tack_schema_guard_probe"
	schemaGuardProbeStatement = "CREATE TABLE " + schemaGuardProbeTable + " (id int)"
	schemaGuardRefusalState   = "42501"
	schemaGuardRefusalText    = "schema change refused"
)

type schemaGuardResult struct {
	clispec.ResultMarker
	Command     string `json:"command"`
	SessionRole string `json:"session_role"`
	ProbeRole   string `json:"probe_role"`
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
	sessionRole, err := requireMigratorMembership(ctx, pool)
	if err != nil {
		return err
	}
	if _, err := removeSchemaGuardProbe(ctx, pool); err != nil {
		return err
	}
	refused, probeErr := attemptSchemaGuardProbe(ctx, pool)
	remained, err := removeSchemaGuardProbe(ctx, pool)
	switch {
	case probeErr != nil:
		return probeErr
	case err != nil:
		return err
	case remained:
		leftover := fmt.Errorf("the schema guard refused %q, but %s remained", schemaGuardProbeStatement, schemaGuardProbeTable)
		logger.ErrorContext(ctx, "schema_guard.probe_remained", slog.String("err", leftover.Error()))
		return leftover
	}
	logger.InfoContext(ctx, "schema_guard.refused",
		slog.String("session_role", sessionRole), slog.String("probe_role", schemaGuardProbeRole),
		slog.String("sqlstate", refused.Code))
	result := schemaGuardResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.audit.prove-schema-guard",
		SessionRole: sessionRole, ProbeRole: schemaGuardProbeRole, Statement: schemaGuardProbeStatement,
		SQLState: refused.Code, Message: refused.Message,
	}
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		logger.ErrorContext(ctx, "schema_guard.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the schema guard proof: %w", err)
	}
	return nil
}
