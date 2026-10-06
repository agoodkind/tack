package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

func auditSchemaGuardProofOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:     clispec.Name{Canonical: "prove-schema-guard", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsAuditSchemaGuardProof), Mutates: true},
		Group:    auditOpsGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Verify that the schema guard refuses a CREATE TABLE statement",
		Long: "Attempt CREATE TABLE audit.tack_schema_guard_probe (id int) using " +
			"the required engine superuser login in DATABASE_URL. Success requires " +
			"SQLSTATE 42501 (insufficient_privilege), a message containing " +
			"\"schema change refused\", and no probe table after the refusal. " +
			"The command drops an existing probe table as tack_migrator. " +
			"Success reports the session role, statement, SQLSTATE, and message " +
			"as JSON. With --execute, the audit choke-point records pending and " +
			"then ok or error under ops.audit_schema_guard_proof. Without --execute, " +
			"the command prints the operator and " +
			"\"would run: ops.audit_schema_guard_proof\".",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New:      func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runSchemaGuardProof(ctx, f.Cfg, sink)
		},
	}
}
