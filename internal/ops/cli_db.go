package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// dbOpsGroup holds the break-glass path to the production database. Direct
// access from the host (root SSH into the database container) leaves no
// ledger row, which is how the 2026-06-08 incident happened; this group is
// the recorded replacement, and closing the raw path is what makes it the
// only one (TACK-327).
var dbOpsGroup = &clispec.Group{
	Use: "db", Short: "Recorded break-glass access to the database", Long: "", Parent: opsGroup,
}

type dbSQLInput struct {
	clispec.InputMarker
	Statement string
	Reason    string
}

func dbSQLOp(f *cli.Factory) clispec.Operation[dbSQLInput] {
	return clispec.Operation[dbSQLInput]{
		Name:    clispec.Name{Canonical: "sql", CLIOverride: ""},
		Audit:   audit.Spec{Verb: string(audit.VerbOpsDBBreakGlass), Mutates: true},
		Group:   dbOpsGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Run one SQL statement against the database, recorded and mailed",
		Long: "Runs one statement as the deployment's database administrator. The " +
			"operator, the reason, and the statement are recorded in the ledger, " +
			"and the alarm address is mailed before the statement runs; a mail " +
			"that cannot be delivered refuses the statement. Nothing runs " +
			"without --execute; without it the command reports what it would run.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[dbSQLInput]{
			clispec.StringParam("statement", "the one SQL statement to run", "", true,
				func(input *dbSQLInput, value string) { input.Statement = value }),
			clispec.StringParam("reason", "why the database is being reached outside the product; recorded and mailed", "", true,
				func(input *dbSQLInput, value string) { input.Reason = value }),
		},
		New: func() dbSQLInput {
			return dbSQLInput{InputMarker: clispec.InputMarker{}, Statement: "", Reason: ""}
		},
		DryRun: func(ctx context.Context, input dbSQLInput, sink clispec.ResultSink) error {
			return runDBSQL(ctx, dbSQLDeps{cfg: f.Cfg, outbox: f.AuditOutbox(), identity: f.OperatorIdentitySource()}, input, sink, false)
		},
		Run: func(ctx context.Context, input dbSQLInput, sink clispec.ResultSink) error {
			return runDBSQL(ctx, dbSQLDeps{cfg: f.Cfg, outbox: f.AuditOutbox(), identity: f.OperatorIdentitySource()}, input, sink, true)
		},
	}
}
