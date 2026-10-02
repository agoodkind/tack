package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// dbPlanGroup is the `ops db plan` family. A plan lists the statements that
// `ops db sql --plan-id` may run with one mail at open and one at close.
var dbPlanGroup = &clispec.Group{
	Use: "plan", Short: "Open and close a mailed plan of break-glass SQL statements", Long: "", Parent: dbOpsGroup,
}

func dbPlanOpenOp(f *cli.Factory) clispec.Operation[dbPlanOpenInput] {
	return clispec.Operation[dbPlanOpenInput]{
		Name:     clispec.Name{Canonical: "open", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDBPlanOpen), Mutates: true},
		Group:    dbPlanGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Mail a plan of SQL statements and open it for ops db sql --plan-id",
		Long: "Reads one SQL statement per non-empty line of --plan; lines that start " +
			"with -- are comments. With --execute the command mails the plan ID, the " +
			"statements, the SHA-256 of the file, the reason, the principal, and the " +
			"expiry to the alarm address, then writes the open row. A mail that " +
			"cannot be delivered fails the command before the open row exists. " +
			"Without --execute the command reports the plan it would open.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[dbPlanOpenInput]{
			clispec.StringParam("plan", "path of the plan file, one SQL statement per line", "", true,
				func(input *dbPlanOpenInput, value string) { input.Plan = value }),
			clispec.StringParam("reason", "why the plan runs statements outside the product; stored and mailed", "", true,
				func(input *dbPlanOpenInput, value string) { input.Reason = value }),
			clispec.StringParam("expires-after", "positive Go duration after which the plan refuses statements, such as 72h", "", true,
				func(input *dbPlanOpenInput, value string) { input.ExpiresAfter = value }),
		},
		New: func() dbPlanOpenInput {
			return dbPlanOpenInput{InputMarker: clispec.InputMarker{}, Plan: "", Reason: "", ExpiresAfter: ""}
		},
		DryRun: func(ctx context.Context, input dbPlanOpenInput, sink clispec.ResultSink) error {
			return runDBPlanOpen(ctx, dbSQLDeps{cfg: f.Cfg, outbox: f.AuditOutbox(), identity: f.OperatorIdentitySource()}, input, sink, false)
		},
		Run: func(ctx context.Context, input dbPlanOpenInput, sink clispec.ResultSink) error {
			return runDBPlanOpen(ctx, dbSQLDeps{cfg: f.Cfg, outbox: f.AuditOutbox(), identity: f.OperatorIdentitySource()}, input, sink, true)
		},
	}
}

func dbPlanCloseOp(f *cli.Factory) clispec.Operation[dbPlanCloseInput] {
	return clispec.Operation[dbPlanCloseInput]{
		Name:     clispec.Name{Canonical: "close", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDBPlanClose), Mutates: true},
		Group:    dbPlanGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Mail the summary of a plan and close it",
		Long: "With --execute the command waits up to --wait for the relay to send the " +
			"events in the operator outbox and for the audit consumer to commit past the " +
			"audit topic high-water marks, then reads the plan rows through the ledger " +
			"reader. Past --wait, or with a plan row in the audit dead-letter table, it " +
			"mails that the summary is incomplete and fails. It then refuses a closer other than " +
			"the principal that opened the plan, with a refused row and a mail. It then " +
			"writes the close row and mails the alarm address each statement run or refused " +
			"under the plan with its outcome and the --postcheck text. A summary mail that " +
			"cannot be delivered fails the command after the close row exists. A plan closed after " +
			"its expiry has \"expired\" in the summary subject and body. Without --execute " +
			"the command reports the plan it would close.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[dbPlanCloseInput]{
			clispec.StringParam("plan-id", "ID that ops db plan open printed", "", true,
				func(input *dbPlanCloseInput, value string) { input.PlanID = value }),
			clispec.StringParam("postcheck", "result of the closing check; included in the summary mail", "", true,
				func(input *dbPlanCloseInput, value string) { input.Postcheck = value }),
			clispec.StringParam("wait", "positive Go duration that close waits for the audit consumer", dbPlanProjectionWait.String(), false,
				func(input *dbPlanCloseInput, value string) { input.Wait = value }),
		},
		New: func() dbPlanCloseInput {
			return dbPlanCloseInput{InputMarker: clispec.InputMarker{}, PlanID: "", Postcheck: "", Wait: ""}
		},
		DryRun: func(ctx context.Context, input dbPlanCloseInput, sink clispec.ResultSink) error {
			return runDBPlanClose(ctx, dbSQLDeps{cfg: f.Cfg, outbox: f.AuditOutbox(), identity: f.OperatorIdentitySource()}, input, sink, false)
		},
		Run: func(ctx context.Context, input dbPlanCloseInput, sink clispec.ResultSink) error {
			return runDBPlanClose(ctx, dbSQLDeps{cfg: f.Cfg, outbox: f.AuditOutbox(), identity: f.OperatorIdentitySource()}, input, sink, true)
		},
	}
}
