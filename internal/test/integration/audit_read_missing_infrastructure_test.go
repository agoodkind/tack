package integration_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// TestAuditReadRefusesMissingInfrastructure checks ordinary reads and the bootstrap preview.
func TestAuditReadRefusesMissingInfrastructure(t *testing.T) {
	empty := testenv.StartEmptyLedger(t, "tack-test-empty-read-"+uuid.NewString()[:8])
	t.Setenv("DATABASE_URL", empty.DSN)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load test configuration: %v", err)
	}
	cfg.AuditReaderDSN = empty.DSN
	pool, err := pgxpool.New(t.Context(), empty.DSN)
	if err != nil {
		t.Fatalf("open empty ledger: %v", err)
	}
	t.Cleanup(pool.Close)
	probeSQL := readTestSQL(t, "audit_empty_infrastructure_probe.sql")
	probe := func(ctx context.Context) (audit.InfrastructureState, error) {
		var state audit.InfrastructureState
		err := pool.QueryRow(ctx, probeSQL).Scan(&state.OutboxTableExists, &state.OperatorLoginExists, &state.OperatorReadsExist)
		return state, err
	}
	state, err := probe(t.Context())
	if err != nil || state.OutboxTableExists || state.OperatorLoginExists || state.OperatorReadsExist {
		t.Fatalf("empty infrastructure = %+v, error %v", state, err)
	}
	run := func(args ...string) (string, error) {
		output := &bytes.Buffer{}
		factory := cli.System(cfg)
		factory.Out = output
		factory.Err = &bytes.Buffer{}
		factory.SetAuditReads(audit.NewPoolReadLifecycle(pool))
		factory.SetAuditInfrastructureProbe(probe)
		root := nodeAuditCommandRoot(factory)
		root.SetContext(t.Context())
		root.SetArgs(append([]string{"--operator-id", uuid.Must(uuid.NewV7()).String(), "--operator-email", "operator@example.test"}, args...))
		err := root.Execute()
		return output.String(), err
	}
	output, err := run("--execute", "ops", "audit", "manifest-parts", "--org", uuid.Must(uuid.NewV7()).String(), "--event-id", uuid.Must(uuid.NewV7()).String())
	if !errors.Is(err, audit.ErrReadNotAdmitted) || output != "" {
		t.Fatalf("ordinary read output = %q, error = %v; want admission refusal", output, err)
	}
	output, err = run("--execute", "ops", "deploy", "verify", "--service", "invalid")
	if !errors.Is(err, audit.ErrReadNotAdmitted) || output != "" {
		t.Fatalf("deploy verification output = %q, error = %v; want admission refusal", output, err)
	}
	output, err = run("--execute", "ops", "audit", "manifest-parts", "--org", uuid.Must(uuid.NewV7()).String(), "--event-id", uuid.Must(uuid.NewV7()).String(), "--verify-payloads")
	if !errors.Is(err, audit.ErrReadNotAdmitted) || output != "" {
		t.Fatalf("manifest verification output = %q, error = %v; want admission refusal", output, err)
	}
	output, err = run("ops", "ledger", "audit-bootstrap")
	if err != nil || !strings.Contains(output, "audit") {
		t.Fatalf("bootstrap preview output = %q, error = %v", output, err)
	}
}
