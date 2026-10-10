package ops_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

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

const killtestSchemaCountQuery = `SELECT count(*) FROM pg_namespace WHERE nspname = 'killtest'`

type killtestSchemaReport struct {
	DryRun bool                     `json:"dry_run"`
	Result ops.KilltestSchemaResult `json:"result"`
}

func TestDropKilltestSchemaDropsTheSchemaOnce(t *testing.T) {
	dsn := testenv.Ledger(t)
	admin := testenv.LedgerPool(t, dsn)
	createKilltestSchema(t, admin)

	planned, err := runDropKilltestSchema(t, dsn, admin, false)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	marker := ops.KilltestSchemaTable{Name: "marker", Rows: 3}
	if !planned.DryRun || !planned.Result.Exists || planned.Result.Owner != "yugabyte" || planned.Result.Dropped ||
		len(planned.Result.Tables) != 1 || planned.Result.Tables[0] != marker {
		t.Fatalf("dry run report = %+v, want the schema owned by yugabyte with %+v and no drop", planned, marker)
	}
	if rows := countRows(t.Context(), t, admin, `SELECT count(*) FROM killtest.marker`); rows != 3 {
		t.Fatalf("killtest.marker has %d rows after the dry run, want 3", rows)
	}

	applied, err := runDropKilltestSchema(t, dsn, admin, true)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !applied.Result.Dropped || len(applied.Result.Tables) != 1 || applied.Result.Tables[0] != marker {
		t.Fatalf("execute report = %+v, want the drop of %+v", applied, marker)
	}
	if schemas := countRows(t.Context(), t, admin, killtestSchemaCountQuery); schemas != 0 {
		t.Fatalf("the killtest schema remains after the drop")
	}

	rerun, err := runDropKilltestSchema(t, dsn, admin, true)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if rerun.Result.Exists || rerun.Result.Dropped || len(rerun.Result.Tables) != 0 {
		t.Fatalf("second execute report = %+v, want an absent schema and no drop", rerun)
	}
}

func TestDropKilltestSchemaRefusesOtherObjects(t *testing.T) {
	dsn := testenv.Ledger(t)
	admin := testenv.LedgerPool(t, dsn)
	createKilltestSchema(t, admin,
		`CREATE VIEW killtest.marker_view AS SELECT id FROM killtest.marker`,
		`CREATE FUNCTION killtest.marker_count() RETURNS bigint LANGUAGE sql AS 'SELECT count(*) FROM killtest.marker'`)

	_, err := runDropKilltestSchema(t, dsn, admin, true)
	if err == nil || !strings.Contains(err.Error(), "relation marker_view") || !strings.Contains(err.Error(), "function marker_count") {
		t.Fatalf("execute with a view and a function: err = %v, want a refusal that lists both", err)
	}
	if schemas := countRows(t.Context(), t, admin, killtestSchemaCountQuery); schemas != 1 {
		t.Fatalf("the killtest schema is gone after the refusal")
	}
}

func TestDropKilltestSchemaRefusesADependentInPublic(t *testing.T) {
	dsn := testenv.Ledger(t)
	admin := testenv.LedgerPool(t, dsn)
	createKilltestSchema(t, admin, `CREATE VIEW public.tack560_marker_view AS SELECT id FROM killtest.marker`)

	_, err := runDropKilltestSchema(t, dsn, admin, true)
	if err == nil || !strings.Contains(err.Error(), "tack560_marker_view") {
		t.Fatalf("execute with a dependent view in public: err = %v, want a refusal that lists the view", err)
	}
	if schemas := countRows(t.Context(), t, admin, killtestSchemaCountQuery); schemas != 1 {
		t.Fatalf("the killtest schema is gone after the refusal")
	}
}

func TestDropKilltestSchemaRefusesTheMigratorLogin(t *testing.T) {
	dsn := testenv.Ledger(t)
	admin := testenv.LedgerPool(t, dsn)
	cfg := &config.Config{DatabaseURL: dsn}
	for _, generated := range []*string{
		&cfg.AuditWriterPassword, &cfg.AuditReaderPassword, &cfg.AuditRedactorPassword,
		&cfg.AuditOperatorPassword, &cfg.AppPassword, &cfg.MigratorPassword,
	} {
		*generated = uuid.NewString()
	}
	if err := ops.RunAuditSeedRoles(t.Context(), cfg); err != nil {
		t.Fatalf("seed-roles as the engine superuser: %v", err)
	}
	createKilltestSchema(t, admin)

	_, err := runDropKilltestSchema(t, loginDSN(t, dsn, postgres.MigratorRole, cfg.MigratorPassword), admin, true)
	if err == nil || !strings.Contains(err.Error(), "the login "+postgres.MigratorRole+" cannot drop") ||
		!strings.Contains(err.Error(), "superuser") {
		t.Fatalf("execute as %s: err = %v, want a refusal that states the login and the right", postgres.MigratorRole, err)
	}
	if schemas := countRows(t.Context(), t, admin, killtestSchemaCountQuery); schemas != 1 {
		t.Fatalf("the killtest schema is gone after the refusal")
	}
}

func createKilltestSchema(t *testing.T, admin *pgxpool.Pool, extra ...string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := admin.Exec(context.WithoutCancel(t.Context()), `DROP SCHEMA IF EXISTS killtest CASCADE`); err != nil {
			t.Errorf("remove the killtest schema: %v", err)
		}
	})
	statements := append([]string{
		`CREATE SCHEMA killtest`,
		`CREATE TABLE killtest.marker (id int PRIMARY KEY)`,
		`INSERT INTO killtest.marker (id) VALUES (1), (2), (3)`,
	}, extra...)
	for _, statement := range statements {
		if _, err := admin.Exec(t.Context(), statement); err != nil {
			t.Fatalf("%q: %v", statement, err)
		}
	}
}

func runDropKilltestSchema(t *testing.T, dsn string, outbox *pgxpool.Pool, execute bool) (killtestSchemaReport, error) {
	t.Helper()
	factory := cli.System(&config.Config{DatabaseURL: dsn})
	var output bytes.Buffer
	factory.Out = &output
	factory.SetAuditOutbox(audit.NewPoolOutbox(outbox))
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, rendered := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(rendered)
	}
	arguments := []string{
		"--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
		"--operator-email", "operator@example.com", "--operator-name", "Killtest Schema Test",
		"ops", "backfill", "once-drop-killtest-schema",
	}
	if execute {
		arguments = append(arguments, "--execute")
	}
	root.SetArgs(arguments)
	var report killtestSchemaReport
	if err := root.Execute(); err != nil {
		return report, err
	}
	_, document, found := strings.Cut(output.String(), "{")
	if !found {
		t.Fatalf("the command printed no JSON report: %q", output.String())
	}
	if err := json.Unmarshal([]byte("{"+document), &report); err != nil {
		t.Fatalf("decode the report %q: %v", output.String(), err)
	}
	return report, nil
}
