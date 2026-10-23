package integration_test

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
)

// coverageDeliveryTimeout limits how long waitForRows polls the audit ledger
// for a matching event.
const coverageDeliveryTimeout = time.Minute

// coverageEnv provides the server, ledger, database pools, and test nodes for
// audit coverage cases. It also tracks command audit classes and whether the
// environment includes OpenSearch.
type coverageEnv struct {
	server         *publicServer
	ledger         publicLedger
	admin          *pgxpool.Pool
	commandClasses map[string]string
	nodes          *coverageFixture
	searchEnabled  bool
	backup         *coverageBackupLedger
}

// startCoverageEnv starts the public server and audit ledger, collects command
// audit classes, and creates test nodes and a backup ledger. It leaves search
// disabled.
func startCoverageEnv(t *testing.T) *coverageEnv {
	t.Helper()
	server := startPublicServer(t, nil)
	env := &coverageEnv{
		server: server, ledger: startPublicLedger(t, server), admin: ledgerAdminPool(t),
		commandClasses: commandClasses(t), nodes: nil, searchEnabled: false, backup: startCoverageBackupLedger(t),
	}
	env.nodes = newCoverageFixture(t, env)
	return env
}

// command executes an ops command with optional configuration changes and
// returns its operator ID, output, and error.
func (env *coverageEnv) command(t *testing.T, configure func(*config.Config), args ...string) (uuid.UUID, string, error) {
	t.Helper()
	return env.commandWith(t, commandInput{args: args, configure: configure, outbox: nil})
}

// commandWith runs the supplied command with --execute and returns its operator
// ID, output, and error.
func (env *coverageEnv) commandWith(t *testing.T, input commandInput) (uuid.UUID, string, error) {
	t.Helper()
	return env.runCommand(t, input, "--execute")
}

// dryRun runs an ops command without --execute and requests JSON output. It
// returns the command output and error.
func (env *coverageEnv) dryRun(t *testing.T, args ...string) (string, error) {
	t.Helper()
	_, output, err := env.runCommand(t, commandInput{args: args, configure: nil, outbox: nil}, "--output", "json")
	return output, err
}

// runCommand runs an ops command with the supplied mode flags and a unique
// operator identity. It applies optional configuration and database pool
// overrides and returns the operator ID, combined output, and error.
func (env *coverageEnv) runCommand(t *testing.T, input commandInput, mode ...string) (uuid.UUID, string, error) {
	t.Helper()
	cfg := *env.server.config
	if input.configure != nil {
		input.configure(&cfg)
	}
	outbox := env.admin
	if input.outbox != nil {
		outbox = input.outbox
	}
	args := input.args
	operatorID := uuid.Must(uuid.NewV7())
	factory := cli.System(&cfg)
	var output bytes.Buffer
	factory.Out, factory.Err = &output, &output
	factory.SetAuditOutbox(audit.NewPoolOutbox(outbox))
	factory.SetAuditReads(audit.NewPoolReadLifecycle(outbox))
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, rendered := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(rendered)
	}
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	root.SetArgs(slices.Concat(mode, []string{
		"--operator-id", operatorID.String(), "--operator-email", "coverage@example.test", "--operator-name", "Coverage Test",
	}, args))
	err := root.ExecuteContext(t.Context())
	return operatorID, output.String(), err
}

// waitForRows polls the ledger for an event that matches the evidence, verb,
// and outcome until coverageDeliveryTimeout expires. When the evidence selects
// an operator outbox, it checks that outbox instead and returns nil.
func (env *coverageEnv) waitForRows(t *testing.T, evidence coverageEvidence, verb audit.Verb, outcome audit.Outcome) []audit.Row {
	t.Helper()
	if evidence.outbox != nil {
		requireOutboxEvent(t, evidence, verb, outcome)
		return nil
	}
	deadline := clock.Now().Add(coverageDeliveryTimeout)
	for {
		rows, err := env.ledger.stack.reader.Query(t.Context(), audit.QueryFilter{
			OrgID: evidence.orgID, Oldest: env.ledger.since, Latest: clock.Now().Add(time.Hour), Action: "",
			ActorID: evidence.actorID, EntityID: uuid.Nil, RequestID: evidence.requestID, TraceID: "", Limit: 100,
		})
		if err != nil {
			t.Fatalf("The audit ledger query failed instead of returning rows for %+v: %v", evidence, err)
		}
		for _, row := range rows {
			if row.Action == string(verb) && row.Outcome == outcome {
				return rows
			}
		}
		if clock.Now().After(deadline) {
			t.Fatalf("The audit ledger did not record a matching event before the deadline; the evidence, expected verb and outcome, and observed events follow: %+v %s %s: %s", evidence, verb, outcome, describeRows(rows))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// describeRows returns the action and outcome of each ledger row for failure
// messages.
func describeRows(rows []audit.Row) string {
	var described bytes.Buffer
	for _, row := range rows {
		described.WriteString(row.Action + " " + string(row.Outcome) + "; ")
	}
	return described.String()
}

// operatorEvidence selects audit events for the given operator in the system
// organization.
func operatorEvidence(actorID uuid.UUID) coverageEvidence {
	return coverageEvidence{orgID: audit.SystemOrgID(), requestID: "", actorID: actorID, outcome: "", outbox: nil}
}

// requireOutboxEvent requires the selected operator outbox to contain an event
// with the expected actor, verb, and outcome.
func requireOutboxEvent(t *testing.T, evidence coverageEvidence, verb audit.Verb, outcome audit.Outcome) {
	t.Helper()
	events := outboxEventsByQuery(t, evidence.outbox, "runtime_audit_actor_outbox.sql", evidence.actorID.String())
	var described bytes.Buffer
	for _, event := range events {
		if event.Verb == string(verb) && event.Outcome == outcome {
			return
		}
		described.WriteString(event.Verb + " " + string(event.Outcome) + "; ")
	}
	t.Fatalf("The operator outbox did not contain a matching event; the actor ID, expected verb and outcome, and observed events follow: %s %s %s: %s", evidence.actorID, verb, outcome, described.String())
}
