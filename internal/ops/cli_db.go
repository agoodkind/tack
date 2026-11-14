package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// dbOpsGroup defines recorded emergency database access. Direct host access
// through root SSH into the database container does not create ledger rows.
// Restrict that access to require this recorded command (TACK-327).
var dbOpsGroup = &clispec.Group{
	Use:    "db",
	Short:  "Recorded break-glass access to the database",
	Long:   "",
	Parent: opsGroup,
}

type dbSQLInput struct {
	clispec.InputMarker
	Statement string
	Reason    string
}

func dbSQLOp(f *cli.Factory) clispec.Operation[dbSQLInput] {
	return clispec.Operation[dbSQLInput]{
		Name:     clispec.Name{Canonical: "sql", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDBBreakGlass), Mutates: true},
		Group:    dbOpsGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Run one read-only SQL statement and record and email the attempt",
		Long: "Run one statement in a read-only session. The server refuses a " +
			"statement that changes data or schema. Database writes require a " +
			"migration or a reviewed ops command. Before execution, the command " +
			"emails the full statement to the alarm address and records the " +
			"operator, reason, and SQL operation in the ledger. The command " +
			"refuses execution if email delivery fails. The command requires " +
			"--execute to run the statement. Without it, the command reports " +
			"what it would run.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[dbSQLInput]{
			clispec.StringParam(
				"statement",
				"the one SQL statement to run",
				"",
				true,
				func(input *dbSQLInput, value string) { input.Statement = value },
			),
			clispec.StringParam(
				"reason",
				"reason for database access outside the product, for the ledger and alarm email",
				"",
				true,
				func(input *dbSQLInput, value string) { input.Reason = value },
			),
		},
		New: func() dbSQLInput {
			return dbSQLInput{InputMarker: clispec.InputMarker{}, Statement: "", Reason: ""}
		},
		DryRun: func(ctx context.Context, input dbSQLInput, sink clispec.ResultSink) error {
			return runDBSQL(
				ctx,
				dbSQLDeps{
					cfg:      f.Cfg,
					outbox:   f.AuditOutbox(),
					identity: f.OperatorIdentitySource(),
				},
				input,
				sink,
				false,
			)
		},
		Run: func(ctx context.Context, input dbSQLInput, sink clispec.ResultSink) error {
			return runDBSQL(
				ctx,
				dbSQLDeps{
					cfg:      f.Cfg,
					outbox:   f.AuditOutbox(),
					identity: f.OperatorIdentitySource(),
				},
				input,
				sink,
				true,
			)
		},
	}
}
