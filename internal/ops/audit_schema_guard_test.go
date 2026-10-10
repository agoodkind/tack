package ops_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
)

const (
	schemaGuardProbe = "audit.tack_schema_guard_probe"
	schemaGuardVerb  = string(audit.VerbOpsAuditSchemaGuardProof)
	roleNamesQuery   = `SELECT coalesce(string_agg(rolname::text, ',' ORDER BY rolname), '') FROM pg_roles`
)

func TestProveSchemaGuardReportsTheRefusalAndRecordsTheRun(t *testing.T) {
	dsn := testenv.Ledger(t)
	pool := schemaGuardPool(t, dsn)
	started := time.Now().UTC()

	output, err := runProveSchemaGuard(t, dsn, pool)
	if err != nil {
		t.Fatalf("ops audit prove-schema-guard: %v\n%s", err, output)
	}
	requireProbeAbsent(t, pool, requireRefusalReport(t, output, "yugabyte"))
	if outcomes := schemaGuardOutcomes(t, pool, started); !slices.Equal(outcomes, []string{"pending", "ok"}) {
		t.Fatalf("outbox outcomes = %v, want pending then ok", outcomes)
	}
}

func TestProveSchemaGuardReportsTheRefusalWithTheMigratorLogin(t *testing.T) {
	adminDSN := testenv.Ledger(t)
	pool := schemaGuardPool(t, adminDSN)
	cfg := &config.Config{DatabaseURL: adminDSN}
	for _, generated := range []*string{
		&cfg.AuditWriterPassword, &cfg.AuditReaderPassword, &cfg.AuditRedactorPassword,
		&cfg.AuditOperatorPassword, &cfg.AppPassword, &cfg.MigratorPassword,
	} {
		*generated = uuid.NewString()
	}
	if err := ops.RunAuditSeedRoles(t.Context(), cfg); err != nil {
		t.Fatalf("seed-roles as the engine superuser: %v", err)
	}
	started := time.Now().UTC()

	output, err := runProveSchemaGuard(t, loginDSN(t, adminDSN, postgres.MigratorRole, cfg.MigratorPassword), pool)
	if err != nil {
		t.Fatalf("ops audit prove-schema-guard as tack_migrator: %v\n%s", err, output)
	}
	requireProbeAbsent(t, pool, requireRefusalReport(t, output, postgres.MigratorRole))
	if outcomes := schemaGuardOutcomes(t, pool, started); !slices.Equal(outcomes, []string{"pending", "ok"}) {
		t.Fatalf("outbox outcomes = %v, want pending then ok", outcomes)
	}
}

func TestProveSchemaGuardFailsAndRemovesTheProbeWithoutTheGuard(t *testing.T) {
	dsn := testenv.Ledger(t)
	pool := schemaGuardPool(t, dsn)
	setSchemaGuardTriggers(t, pool, "DISABLE")
	t.Cleanup(func() { setSchemaGuardTriggers(t, pool, "ENABLE") })
	var rolesBefore, rolesAfter string
	if err := pool.QueryRow(t.Context(), roleNamesQuery).Scan(&rolesBefore); err != nil {
		t.Fatalf("read the roles before the command: %v", err)
	}
	started := time.Now().UTC()

	output, err := runProveSchemaGuard(t, dsn, pool)
	if err == nil || !strings.Contains(err.Error(), "accepted") {
		t.Fatalf("ops audit prove-schema-guard without the guard: err = %v, want the acceptance reported\n%s", err, output)
	}
	requireProbeAbsent(t, pool, "")
	if err := pool.QueryRow(t.Context(), roleNamesQuery).Scan(&rolesAfter); err != nil {
		t.Fatalf("read the roles after the command: %v", err)
	}
	if rolesAfter != rolesBefore {
		t.Fatalf("roles after the command = %s, want the roles before it: %s", rolesAfter, rolesBefore)
	}
	if outcomes := schemaGuardOutcomes(t, pool, started); !slices.Equal(outcomes, []string{"pending", "error"}) {
		t.Fatalf("outbox outcomes = %v, want pending then error", outcomes)
	}
}

func schemaGuardPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open the ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func runProveSchemaGuard(t *testing.T, dsn string, pool *pgxpool.Pool) (string, error) {
	t.Helper()
	factory := cli.System(&config.Config{DatabaseURL: dsn})
	var output bytes.Buffer
	factory.Out = &output
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, rendered := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(rendered)
	}
	root.SetArgs([]string{
		"--execute", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
		"--operator-email", "operator@example.com", "--operator-name", "Schema Guard Test",
		"ops", "audit", "prove-schema-guard",
	})
	err := root.Execute()
	return output.String(), err
}

func requireRefusalReport(t *testing.T, output, sessionRole string) string {
	t.Helper()
	var report struct {
		SessionRole string `json:"session_role"`
		ProbeRole   string `json:"probe_role"`
		SQLState    string `json:"sqlstate"`
		Message     string `json:"message"`
	}
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("decode the report %q: %v", output, err)
	}
	if report.SQLState != "42501" || !strings.Contains(report.Message, "schema change refused") ||
		report.SessionRole != sessionRole || report.ProbeRole == "" || report.ProbeRole == sessionRole {
		t.Fatalf("report = %+v, want the guard refusal of a probe role for the %s login", report, sessionRole)
	}
	return report.ProbeRole
}

func requireProbeAbsent(t *testing.T, pool *pgxpool.Pool, probeRole string) {
	t.Helper()
	var tablePresent, rolePresent bool
	if err := pool.QueryRow(t.Context(),
		`SELECT to_regclass($1) IS NOT NULL, EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $2)`,
		schemaGuardProbe, probeRole).Scan(&tablePresent, &rolePresent); err != nil {
		t.Fatalf("check for %s and role %q: %v", schemaGuardProbe, probeRole, err)
	}
	if tablePresent || rolePresent {
		t.Fatalf("after the command, table %s present = %v and role %q present = %v, want both absent",
			schemaGuardProbe, tablePresent, probeRole, rolePresent)
	}
}

func schemaGuardOutcomes(t *testing.T, pool *pgxpool.Pool, since time.Time) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(),
		`SELECT event->>'outcome' FROM public.ops_outbox
		  WHERE event->>'verb' = $1 AND created_at >= $2 ORDER BY created_at, event->>'occurred_at'`,
		schemaGuardVerb, since)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	defer rows.Close()
	var outcomes []string
	for rows.Next() {
		var outcome string
		if err := rows.Scan(&outcome); err != nil {
			t.Fatalf("scan an outbox row: %v", err)
		}
		outcomes = append(outcomes, outcome)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the outbox rows: %v", err)
	}
	return outcomes
}

func setSchemaGuardTriggers(t *testing.T, pool *pgxpool.Pool, state string) {
	t.Helper()
	for _, trigger := range []string{"tack_audit_schema_guard", "tack_audit_schema_guard_drop"} {
		if _, err := pool.Exec(context.WithoutCancel(t.Context()), "ALTER EVENT TRIGGER "+trigger+" "+state); err != nil {
			t.Fatalf("%s event trigger %s: %v", state, trigger, err)
		}
	}
}
