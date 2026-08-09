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
		Name:    clispec.Name{Canonical: "seed-roles", CLIOverride: ""},
		Audit:   audit.Spec{Verb: string(audit.VerbAuditRolesSeed), Mutates: true},
		Group:   auditOpsGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Create or rotate the LOGIN audit roles (tack_audit_writer/reader/redactor)",
		Long: "Idempotently create or rotate the LOGIN audit roles used by the app " +
			"and the audit-consumer to write and read the compliance ledger.",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New:      func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ noInput, _ clispec.ResultSink) error {
			return RunAuditSeedRoles(ctx, f.Cfg)
		},
	}
}
