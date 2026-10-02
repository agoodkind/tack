// ledger_audit_bootstrap.go runs `ops ledger audit-bootstrap`: on an empty
// ledger, it applies the migrations and seeds the audit login roles, which
// creates the operator outbox and the operator login that every later operator
// command records through. It uses only cfg.DatabaseURL and the login
// passwords: no FoundationDB, no Docker, no continuous backup, and no product
// seed.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/migrations"
)

const (
	// ledgerAuditBootstrapCommand prefixes this command's errors.
	ledgerAuditBootstrapCommand = "ops ledger audit-bootstrap"
	// ledgerOperatorOutboxTable is the outbox the choke-point writes to.
	ledgerOperatorOutboxTable = "public.ops_outbox"
	// ledgerOperatorLogin is the login the operator outbox connects as.
	ledgerOperatorLogin = "tack_audit_operator"
	// ledgerAuditPresenceQuery reads whether the migration table, the outbox
	// table, and the operator login exist.
	ledgerAuditPresenceQuery = "SELECT to_regclass($1) IS NOT NULL, to_regclass($2) IS NOT NULL, " +
		"EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $3)"
)

// ledgerAuditPresence records which parts of the audit infrastructure exist
// on the ledger.
type ledgerAuditPresence struct {
	migrationTable bool
	outboxTable    bool
	operatorLogin  bool
}

// found lists the parts that exist, each as the table or role an operator
// would look up.
func (p ledgerAuditPresence) found() []string {
	var present []string
	if p.migrationTable {
		present = append(present, "migration table "+goose.TableName())
	}
	if p.outboxTable {
		present = append(present, "table "+ledgerOperatorOutboxTable)
	}
	if p.operatorLogin {
		present = append(present, "role "+ledgerOperatorLogin)
	}
	return present
}

// readLedgerAuditPresence reads, through cfg.DatabaseURL, the refusal test of
// this command: the ledger counts as populated when any one of these exists:
//   - the migration version table that postgres.Migrate writes (goose's
//     table, resolved through the connection's search path the way the
//     migrator resolves it),
//   - public.ops_outbox,
//   - the role tack_audit_operator.
//
// A failed connection is an error and never reads as an empty ledger.
func readLedgerAuditPresence(ctx context.Context, databaseURL string) (ledgerAuditPresence, error) {
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.audit_bootstrap.connect_failed", slog.String("err", err.Error()))
		return ledgerAuditPresence{}, fmt.Errorf("connect to read the ledger audit infrastructure: %w", err)
	}
	defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
	var presence ledgerAuditPresence
	row := connection.QueryRow(ctx, ledgerAuditPresenceQuery,
		goose.TableName(), ledgerOperatorOutboxTable, ledgerOperatorLogin)
	if err := row.Scan(&presence.migrationTable, &presence.outboxTable, &presence.operatorLogin); err != nil {
		slog.ErrorContext(ctx, "ops.ledger.audit_bootstrap.read_failed", slog.String("err", err.Error()))
		return ledgerAuditPresence{}, fmt.Errorf("read the ledger audit infrastructure: %w", err)
	}
	return presence, nil
}

// runLedgerAuditBootstrap refuses a populated ledger before it changes
// anything, then migrates and seeds the audit roles. With execute false it
// reports the decision and writes nothing.
func runLedgerAuditBootstrap(ctx context.Context, cfg *config.Config, sink clispec.ResultSink, execute bool) error {
	presence, err := readLedgerAuditPresence(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("%s: %w", ledgerAuditBootstrapCommand, err)
	}
	if found := presence.found(); len(found) > 0 {
		refusal := fmt.Errorf("%s: the ledger already contains %s; this command runs only on an empty ledger, and ops provision runs on a populated one",
			ledgerAuditBootstrapCommand, strings.Join(found, ", "))
		slog.ErrorContext(ctx, "ops.ledger.audit_bootstrap.refused", slog.String("err", refusal.Error()))
		return refusal
	}
	if !execute {
		return writeLedgerAuditBootstrapLine(ctx, sink, "the ledger is empty; with --execute this runs the migrations and seeds the audit roles")
	}
	if err := migrateAndSeedAuditRoles(ctx, cfg); err != nil {
		return fmt.Errorf("%s: %w", ledgerAuditBootstrapCommand, err)
	}
	slog.InfoContext(ctx, "ops.ledger.audit_bootstrap.completed")
	return writeLedgerAuditBootstrapLine(ctx, sink, "ledger audit bootstrap complete: migrations applied and audit roles seeded")
}

// writeLedgerAuditBootstrapLine writes one result line through the sink.
func writeLedgerAuditBootstrapLine(ctx context.Context, sink clispec.ResultSink, line string) error {
	if err := sink.WriteText(ctx, line); err != nil {
		slog.ErrorContext(ctx, "ops.ledger.audit_bootstrap.write_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: write result: %w", ledgerAuditBootstrapCommand, err)
	}
	return nil
}

// migrateAndSeedAuditRoles applies the migrations and then creates or rotates
// the audit login roles, through cfg.DatabaseURL only. `ops provision` and
// `ops ledger audit-bootstrap` both run it.
func migrateAndSeedAuditRoles(ctx context.Context, cfg *config.Config) error {
	slog.InfoContext(ctx, "provision.migrate.start")
	if err := postgres.Migrate(ctx, cfg.DatabaseURL, migrations.FS); err != nil {
		slog.ErrorContext(ctx, "provision.migrate.failed", slog.String("err", err.Error()))
		return fmt.Errorf("provision migrate: %w", err)
	}

	slog.InfoContext(ctx, "provision.seed_roles.start")
	if err := RunAuditSeedRoles(ctx, cfg); err != nil {
		return fmt.Errorf("provision seed roles: %w", err)
	}
	return nil
}
