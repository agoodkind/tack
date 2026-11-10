package ops

import (
	"context"
	"fmt"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// ledgerGroup contains the commands a deploy runs on a ledger guest: the
// bootstrap of an empty ledger, and the preparation of and wait on this
// guest's own node around a restart.
var ledgerGroup = &clispec.Group{
	Use: "ledger", Short: "Bootstrap an empty ledger, and prepare and wait on this guest's ledger node around a restart",
	Long: "", Parent: opsGroup,
}

// ledgerAuditBootstrapOp declares `ops ledger audit-bootstrap`. The command
// creates the outbox that stores its own record. It declares
// CreatesAuditInfrastructure, and the choke-point writes its intent and
// outcome after it runs.
func ledgerAuditBootstrapOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:     clispec.Name{Canonical: "audit-bootstrap", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit: audit.Spec{
			Verb:                       string(audit.VerbOpsLedgerAuditBootstrap),
			Mutates:                    true,
			CreatesAuditInfrastructure: true,
		},
		Group:   ledgerGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Run the migrations and seed the audit roles on an empty ledger, creating the operator outbox",
		Long: "Refuses before any change when the ledger already contains the migration table, " +
			"public.ops_outbox, or the tack_audit_operator role; ops provision runs on a populated " +
			"ledger. On an empty ledger it runs the migrations and seeds the audit login roles through " +
			"DATABASE_URL, with no FoundationDB, Docker, backup, or product seed step. Without --execute " +
			"it reports and writes nothing.",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New:      func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		DryRun: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runLedgerAuditBootstrap(ctx, f.Cfg, sink, false)
		},
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runLedgerAuditBootstrap(ctx, f.Cfg, sink, true)
		},
	}
}

// ledgerNodeWaitWindows parses the two flags and refuses a window that
// could never end the wait or never let it read.
func ledgerNodeWaitWindows(in ledgerNodeWaitInput) (stall, poll time.Duration, err error) {
	stall, err = time.ParseDuration(in.Stall)
	if err != nil || stall <= 0 {
		return 0, 0, fmt.Errorf("--stall %q is not a positive duration", in.Stall)
	}
	poll, err = time.ParseDuration(in.Poll)
	if err != nil || poll <= 0 {
		return 0, 0, fmt.Errorf("--poll %q is not a positive duration", in.Poll)
	}
	return stall, poll, nil
}
