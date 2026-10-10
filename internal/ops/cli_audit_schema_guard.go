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
		Long: "The command uses DATABASE_URL with a login in tack_migrator. The command runs " +
			"CREATE TABLE audit.tack_schema_guard_probe (id int) as tack_schema_guard_probe. " +
			"The command requires SQLSTATE 42501 (insufficient_privilege), a message containing " +
			"\"schema change refused\", and no probe table. The command creates the temporary " +
			"role and grants as tack_migrator. The command removes the probe table, grants, and " +
			"temporary role before and after the probe, including failures. The command returns " +
			"successful proof as JSON. The audit choke-point records pending and then ok or " +
			"error under ops.audit_schema_guard_proof with --execute. The command prints the " +
			"operator and \"would run: ops.audit_schema_guard_proof\" without --execute.",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New:      func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runSchemaGuardProof(ctx, f.Cfg, sink)
		},
	}
}
