package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// auditSeedRolesOp declares `ops audit seed-roles`, which idempotently creates
// or rotates the LOGIN audit roles the app and audit-consumer use to write and
// read the compliance ledger.
func auditSeedRolesOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:     clispec.Name{Canonical: "seed-roles", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbAuditRolesSeed), Mutates: true},
		Group:    auditOpsGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Create or rotate the LOGIN roles (tack_audit_writer/reader/redactor/operator, tack_app, tack_migrator)",
		Long: "Idempotently create or rotate the LOGIN audit roles used by the app " +
			"and the audit-consumer to write and read the compliance ledger, and " +
			"tack_app, the non-superuser login the application's DATABASE_URL uses " +
			"for the auth tables (TACK-180). Sets LOGIN and the password on " +
			"tack_migrator, the non-superuser login of migrations and operator " +
			"commands (TACK-554).",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New:      func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ noInput, _ clispec.ResultSink) error {
			return RunAuditSeedRoles(ctx, f.Cfg)
		},
	}
}

// auditConsumerOffsetsOp declares `ops audit consumer-offsets`. It reports
// the audit events that are produced and not yet in the ledger: the committed
// lag of every topic partition and the events waiting in both operator
// outboxes.
func auditConsumerOffsetsOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:     clispec.Name{Canonical: "consumer-offsets", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsAuditConsumerOffsets), Reads: true},
		Group:    auditOpsGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Report the audit consumer's committed lag and the operator outbox remainders",
		Long: "Reads audit.consumer_offsets through the ledger reader and the latest offset of " +
			"every audit topic partition from the brokers, and reports the committed lag per " +
			"partition. Also reports the row count and oldest row of public.ops_outbox and the " +
			"entry count of the FoundationDB operator outbox. Run it through the app service, " +
			"which has the reader DSN, the brokers, and FoundationDB.",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New:      func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			report, err := readAuditConsumerOffsets(ctx, f)
			if err != nil {
				return err
			}
			return clispec.WriteJSONValue(ctx, sink, report)
		},
	}
}
