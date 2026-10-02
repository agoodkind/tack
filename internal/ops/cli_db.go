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
	// PlanID is the ID of an open plan that lists the statement. An empty
	// value mails the alarm address before the statement runs.
	PlanID string `exhaustruct:"optional"`
}

func dbSQLOp(f *cli.Factory) clispec.Operation[dbSQLInput] {
	return clispec.Operation[dbSQLInput]{
		Name:     clispec.Name{Canonical: "sql", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDBBreakGlass), Mutates: true},
		Group:    dbOpsGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Run one SQL statement against the database, recorded and mailed",
		Long: "Runs one statement as the deployment's database administrator. The " +
			"operator, the reason, and the statement are recorded in the ledger, " +
			"and the alarm address is mailed before the statement runs; a mail " +
			"that cannot be delivered refuses the statement. Nothing runs " +
			"without --execute; without it the command reports what it would run. " +
			"With --plan-id the command waits up to " + dbPlanOpenRowWait.String() + " for the " +
			"open row of the plan in the operator outbox or the ledger. The statement " +
			"runs without its own mail when the open " +
			"plan lists it and the same principal opened the plan; otherwise the " +
			"command writes a refused row, mails the refusal, and returns an error " +
			"before the statement runs.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[dbSQLInput]{
			clispec.StringParam("statement", "the one SQL statement to run", "", true,
				func(input *dbSQLInput, value string) { input.Statement = value }),
			clispec.StringParam("reason", "why the database is being reached outside the product; recorded and mailed", "", true,
				func(input *dbSQLInput, value string) { input.Reason = value }),
			clispec.StringParam("plan-id", "ID of the open plan that lists this statement (ops db plan open)", "", false,
				func(input *dbSQLInput, value string) { input.PlanID = value }),
		},
		New: func() dbSQLInput {
			return dbSQLInput{InputMarker: clispec.InputMarker{}, Statement: "", Reason: "", PlanID: ""}
		},
		DryRun: func(ctx context.Context, input dbSQLInput, sink clispec.ResultSink) error {
			return runDBSQL(ctx, dbSQLDeps{cfg: f.Cfg, outbox: f.AuditOutbox(), identity: f.OperatorIdentitySource()}, input, sink, false)
		},
		Run: func(ctx context.Context, input dbSQLInput, sink clispec.ResultSink) error {
			return runDBSQL(ctx, dbSQLDeps{cfg: f.Cfg, outbox: f.AuditOutbox(), identity: f.OperatorIdentitySource()}, input, sink, true)
		},
	}
}
