package integration_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/test/integration"
)

const (
	searchReadDeployCommit = "tack-617-read-regression"
	searchReadSession      = "tack-617-test-session"
)

func searchReadCommand(t *testing.T, cfg *config.Config, pool *pgxpool.Pool, operatorID uuid.UUID, command ...string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	factory := cli.System(cfg)
	output := &bytes.Buffer{}
	factory.Out, factory.Err = output, &bytes.Buffer{}
	configureSearchReadAudit(t, factory, pool)
	root := integration.SearchCommandRoot(factory)
	root.SetArgs(append([]string{
		"--execute", "--output", "json", "--operator-id", operatorID.String(),
		"--operator-email", searchSettingsOperatorEmail, "--operator-name", "Search Test",
		"--operator-service", searchReadService(operatorID), "--operator-session", searchReadSession,
		"--deploy-commit", searchReadDeployCommit,
	}, command...))
	return root, output
}

func configureSearchReadAudit(t *testing.T, factory *cli.Factory, pool *pgxpool.Pool) {
	t.Helper()
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	factory.SetAuditReads(audit.NewPoolReadLifecycle(pool))
	probeQuery := readTestSQL(t, "search_read_infrastructure.sql")
	factory.SetAuditInfrastructureProbe(func(ctx context.Context) (audit.InfrastructureState, error) {
		connection, err := pgx.Connect(ctx, factory.Cfg.DatabaseURL)
		if err != nil {
			return audit.InfrastructureState{}, err
		}
		defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
		var state audit.InfrastructureState
		err = connection.QueryRow(ctx, probeQuery).Scan(&state.OutboxTableExists, &state.OperatorLoginExists, &state.OperatorReadsExist)
		return state, err
	})
}

func searchReadService(operatorID uuid.UUID) string { return "tack-search-read-" + operatorID.String() }
