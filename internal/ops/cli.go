package ops

import (
	"context"
	"strings"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// The operator ops command groups use one *Group value per parent. The renderer
// attaches each parent's subcommands to one Cobra command.
var (
	opsGroup      = &clispec.Group{Use: "ops", Short: "Operator maintenance commands for node and storage state", Long: "", Parent: nil}
	inspectGroup  = &clispec.Group{Use: "inspect", Short: "Read, find, and query node and storage state", Long: "", Parent: opsGroup}
	verifyGroup   = &clispec.Group{Use: "verify", Short: "Check node consistency and constraints", Long: "", Parent: opsGroup}
	validateGroup = &clispec.Group{Use: "validate", Short: "Check repair applicability for a node", Long: "", Parent: opsGroup}
	repairGroup   = &clispec.Group{Use: "repair", Short: "Preview and apply targeted repairs", Long: "", Parent: opsGroup}
	auditOpsGroup = &clispec.Group{Use: "audit", Short: "Compliance audit operations", Long: "", Parent: opsGroup}
	authOpsGroup  = &clispec.Group{Use: "auth", Short: "Issue, list, and revoke API tokens for existing users", Long: "", Parent: opsGroup}
	batchGroup    = &clispec.Group{Use: "batch", Short: "Run registered batch maintenance operations", Long: "", Parent: opsGroup}
)

// noInput represents input for commands without flags or positional arguments.
type noInput struct {
	clispec.InputMarker
}

// RegisterCommands registers the operator ops commands in reg.
//
// The registry combines declared leaf operations with commands that build their
// own Cobra trees. The backup subtree rejects invocation of its bare parent
// command.
//
// Registration uses these forms:
//   - clispec.Register adds a declared operation.
//   - reg.AddHandwritten adds a command builder.
func RegisterCommands(reg *clispec.Registry, f *cli.Factory) {
	clispec.Register(reg, inspectReadOp(f))
	clispec.Register(reg, inspectFindOp(f))
	clispec.Register(reg, inspectQueryOp(f))
	clispec.Register(reg, verifyNodeOp(f))
	clispec.Register(reg, strictValidationOp(f))
	clispec.Register(reg, validateNodeOp(f))
	clispec.Register(reg, repairClassesOp(f))
	clispec.Register(reg, repairPreviewOp(f))
	clispec.Register(reg, repairApplyOp(f))
	clispec.Register(reg, repairReferenceUniquenessOp(f))
	reg.AddHandwritten(clispec.HandwrittenCommand{Group: auditOpsGroup, Build: auditSeedRolesCommand})
	clispec.Register(reg, auditDLQInspectOp(f))
	clispec.Register(reg, auditDLQReplayOp(f))
	clispec.Register(reg, auditConsumerOffsetsOp(f))
	clispec.Register(reg, auditManifestPartsOp(f))
	clispec.Register(reg, auditSchemaGuardProofOp(f))
	clispec.Register(reg, authTokenCreateOp(f))
	clispec.Register(reg, authTokenListOp(f))
	clispec.Register(reg, authTokenRevokeOp(f))
	clispec.Register(reg, actAsCreateOp(f))
	clispec.Register(reg, dbSQLOp(f))
	clispec.Register(reg, datagenSeedOp(f))
	clispec.Register(reg, datagenSoakOp(f))
	clispec.Register(reg, datagenSearchOp(f))
	clispec.Register(reg, datagenSearchLoadOp(f))
	clispec.Register(reg, datagenSearchMixedOp(f))
	clispec.Register(reg, datagenLegacyLedgerRowOp(f))
	clispec.Register(reg, datagenRepairOp(f))
	reg.AddHandwritten(clispec.HandwrittenCommand{Group: opsGroup, Build: provisionCommand})
	clispec.Register(reg, ledgerAuditBootstrapOp(f))
	clispec.Register(reg, ledgerBootstrapWaitOp(f))
	reg.AddHandwritten(clispec.HandwrittenCommand{Group: ledgerGroup, Build: ledgerNodePrepareCommand})
	reg.AddHandwritten(clispec.HandwrittenCommand{Group: ledgerGroup, Build: ledgerNodeWaitCommand})
	clispec.Register(reg, storeStatusOp(f))
	clispec.Register(reg, storeSetRedundancyOp(f))
	clispec.Register(reg, storeSetCoordinatorsOp(f))
	clispec.Register(reg, storeExcludeOp(f))
	reg.AddHandwritten(clispec.HandwrittenCommand{Group: deployGroup, Build: deployVerifyCommand})
	clispec.Register(reg, searchVerifyOp(f))
	clispec.Register(reg, searchSettingsOp(f))
	reg.AddHandwritten(clispec.HandwrittenCommand{Group: searchGroup, Build: searchProvisionCommand})
	reg.AddHandwritten(clispec.HandwrittenCommand{Group: searchGroup, Build: searchRolloutCommand})
	clispec.Register(reg, searchRollbackOp(f))
	reg.AddHandwritten(clispec.HandwrittenCommand{Group: searchGroup, Build: searchReindexCommand})
	clispec.Register(reg, searchProjectionBackfillOp(f))
	clispec.Register(reg, fixedDefinitionsOp(f))
	clispec.Register(reg, metadataNameBackfillOp(f))
	clispec.Register(reg, orphanNodeBackfillOp(f))
	clispec.Register(reg, nodeIntegrityBackfillOp(f))
	clispec.Register(reg, auditPartitionNamesBackfillOp(f))
	clispec.Register(reg, killtestSchemaBackfillOp(f))
	registerBatchOps(reg, f)
	reg.AddHandwritten(clispec.HandwrittenCommand{Group: opsGroup, Build: backupCommand})
}

// registerBatchOps registers one ops batch leaf for each registered batch
// operation except repair.* helpers, which the repair family exposes. Each leaf
// uses the exact canonical operation name, including dots.
func registerBatchOps(reg *clispec.Registry, f *cli.Factory) {
	for _, op := range List() {
		if strings.HasPrefix(op.Name, "repair.") {
			continue
		}
		name := op.Name
		var dryRun func(ctx context.Context, in noInput, sink clispec.ResultSink) error
		if op.DryRun != nil {
			dryRun = func(ctx context.Context, _ noInput, _ clispec.ResultSink) error {
				return RunDryRun(ctx, f.Cfg, name)
			}
		}
		clispec.Register(reg, clispec.Operation[noInput]{
			Name:     clispec.Name{Canonical: name, CLIOverride: name},
			Lifetime: op.Lifetime,
			Audit:    op.Audit,
			Group:    batchGroup,
			Aliases:  nil,
			Hidden:   false,
			Short:    op.Description,
			Long:     "",
			Examples: nil,
			Args:     nil,
			Params:   nil,
			New:      func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
			DryRun:   dryRun,
			Run: func(ctx context.Context, _ noInput, _ clispec.ResultSink) error {
				return Run(ctx, f.Cfg, name)
			},
		})
	}
}
