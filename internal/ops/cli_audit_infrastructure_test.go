package ops_test

import (
	"bytes"
	_ "embed"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

//go:embed testdata/audit_gate_roles.sql
var gateRoleStateQuery string

const (
	// closedOutboxDSN connects to a loopback port with no listener. Every
	// outbox write through it fails at connect, before any command body runs.
	closedOutboxDSN = "host=::1 port=1 user=tack_audit_operator dbname=tack sslmode=disable connect_timeout=2"
	// unwiredProbeError is the choke-point's error for a command that creates
	// the audit infrastructure when no infrastructure probe is wired.
	unwiredProbeError = "audit infrastructure probe is unwired"
	// readRecordError is the choke-point's error for a read that could not
	// write its record.
	readRecordError = "record read access"
	// intentRecordError is the choke-point's error for a mutating command that
	// could not write its intent.
	intentRecordError = "record command intent"
	// gateOperatorID is the test operator, never a person.
	gateOperatorID = "019dd226-440e-729a-a442-281aaf73ca32"
)

// TestOnlyProvisionAndAuditBootstrapCreateAuditInfrastructure runs every
// audited command in the rendered ops tree with --execute through the real
// choke-point and the real operator outbox. Most commands use an outbox
// pointed at a port with no listener. Seed-roles authenticates to an empty
// disposable ledger. That ledger does not have an operator outbox. Each
// command stops at its first record. A read stops with
// "record read access", a mutating command with "record command intent", and
// a command that declares CreatesAuditInfrastructure stops at the unwired
// infrastructure probe. Only ops provision and ops ledger audit-bootstrap may
// stop at the probe: any other command that ran before its record existed
// would act unrecorded.
func TestOnlyProvisionAndAuditBootstrapCreateAuditInfrastructure(t *testing.T) {
	node := testenv.StartEmptyLedger(t, "tack-test-audit-gate-"+uuid.NewString()[:8])
	cfg := &config.Config{DatabaseURL: node.DSN, AuditOperatorDSN: node.DSN, AuditOperatorPassword: uuid.NewString()}

	creators := map[string]string{}
	for _, path := range auditedCommandPaths(t) {
		outboxPool, err := pgxpool.New(t.Context(), closedOutboxDSN)
		if err != nil {
			t.Fatalf("open the operator outbox pool: %v", err)
		}
		factory := &cli.Factory{Cfg: cfg, In: nil, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
		t.Cleanup(factory.CloseAuditOutbox)
		root := executeGateRoot(t, factory)
		factory.SetOperatorIdentitySource(cli.FlagOperatorSource{Factory: factory})
		factory.SetAuditOutbox(audit.NewPoolOutbox(outboxPool))
		leaf, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("find %v: %v", path, err)
		}
		args := append(slices.Clone(path), placeholderArguments(leaf)...)
		args = append(args, "--execute", "--operator-id", gateOperatorID,
			"--operator-email", "gate@example.test", "--operator-name", "Gate Test")
		root.SetArgs(args)
		runErr := root.Execute()
		message := ""
		if runErr != nil {
			message = runErr.Error()
		}
		if strings.Join(path, " ") == "ops audit seed-roles" {
			var postgresError *pgconn.PgError
			if !errors.As(runErr, &postgresError) || postgresError.Code != "42P01" {
				t.Errorf("seed-roles did not reach the missing outbox table: %v", runErr)
			}
		}
		switch {
		case strings.Contains(message, unwiredProbeError):
			creators[strings.Join(path, " ")] = leaf.Annotations[clispec.AuditVerbAnnotation]
		case strings.Contains(message, readRecordError), strings.Contains(message, intentRecordError):
		default:
			t.Errorf("%v stopped with %q, want a failed record before the command body", path, message)
		}
	}

	want := map[string]string{
		"ops provision":              string(audit.VerbOpsProvision),
		"ops ledger audit-bootstrap": string(audit.VerbOpsLedgerAuditBootstrap),
	}
	if !maps.Equal(creators, want) {
		t.Fatalf("commands that create the audit infrastructure = %v, want %v", creators, want)
	}
	ledger, err := pgxpool.New(t.Context(), node.DSN)
	if err != nil {
		t.Fatalf("open the disposable ledger: %v", err)
	}
	t.Cleanup(ledger.Close)
	roles := []string{"tack_audit_writer", "tack_audit_reader", "tack_audit_redactor", "tack_audit_operator", "tack_app", "tack_migrator"}
	rows, err := ledger.Query(t.Context(), gateRoleStateQuery, roles, []string{})
	if err != nil {
		t.Fatalf("read the disposable login roles: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("seed-roles changed a login role without recording intent")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the disposable login roles: %v", err)
	}
}

// auditedCommandPaths lists the path of every command in the rendered tree
// with an audit verb annotation.
func auditedCommandPaths(t *testing.T) [][]string {
	t.Helper()
	factory := &cli.Factory{Cfg: nil, In: nil, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	root := executeGateRoot(t, factory)
	var paths [][]string
	var walk func(command *cobra.Command, path []string)
	walk = func(command *cobra.Command, path []string) {
		if command.Annotations[clispec.AuditVerbAnnotation] != "" {
			paths = append(paths, slices.Clone(path))
		}
		for _, child := range command.Commands() {
			walk(child, append(slices.Clone(path), child.Name()))
		}
	}
	walk(root, nil)
	if len(paths) == 0 {
		t.Fatal("the rendered tree has no audited command")
	}
	return paths
}

// placeholderArguments returns one value per positional placeholder in the
// command's usage line and one flag value per required flag. Cobra then
// accepts the command line and calls the choke-point.
func placeholderArguments(command *cobra.Command) []string {
	var values []string
	for range strings.Fields(command.Use)[1:] {
		values = append(values, "placeholder")
	}
	command.Flags().VisitAll(func(flag *pflag.Flag) {
		if _, required := flag.Annotations[cobra.BashCompOneRequiredFlag]; required {
			values = append(values, "--"+flag.Name+"=placeholder")
		}
	})
	return values
}
