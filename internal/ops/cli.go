package ops

import (
	"context"
	"strings"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// Command groups for the operator `ops` family. One *Group value per parent so
// the renderer attaches every subcommand under a single cobra parent.
var (
	opsGroup      = &clispec.Group{Use: "ops", Short: "Operator maintenance commands for node and storage state", Long: "", Parent: nil}
	inspectGroup  = &clispec.Group{Use: "inspect", Short: "Read, find, and query node and storage state", Long: "", Parent: opsGroup}
	verifyGroup   = &clispec.Group{Use: "verify", Short: "Check node consistency and constraints", Long: "", Parent: opsGroup}
	validateGroup = &clispec.Group{Use: "validate", Short: "Check repair applicability for a node", Long: "", Parent: opsGroup}
	repairGroup   = &clispec.Group{Use: "repair", Short: "Preview and apply targeted repairs", Long: "", Parent: opsGroup}
	auditOpsGroup = &clispec.Group{Use: "audit", Short: "Compliance audit operations", Long: "", Parent: opsGroup}
	batchGroup    = &clispec.Group{Use: "batch", Short: "Run registered batch maintenance operations", Long: "", Parent: opsGroup}
)

// noInput is the input for commands that take no flags or positional args.
type noInput struct {
	clispec.InputMarker
}

// RegisterCommands adds the whole `ops` family to reg: the declared leaf
// operations plus the hand-written backup and deploy subtrees. Bare backup
// rejects invocation, while deploy carries a default action.
func RegisterCommands(reg *clispec.Registry, f *cli.Factory) {
	clispec.Register(reg, inspectReadOp(f))
	clispec.Register(reg, inspectFindOp(f))
	clispec.Register(reg, inspectQueryOp(f))
	clispec.Register(reg, verifyNodeOp(f))
	clispec.Register(reg, validateNodeOp(f))
	clispec.Register(reg, repairClassesOp(f))
	clispec.Register(reg, repairPreviewOp(f))
	clispec.Register(reg, repairApplyOp(f))
	clispec.Register(reg, repairReferenceUniquenessOp(f))
	clispec.Register(reg, auditSeedRolesOp(f))
	clispec.Register(reg, auditReconstructReferenceRepairOp(f))
	clispec.Register(reg, datagenSeedOp(f))
	clispec.Register(reg, datagenSoakOp(f))
	clispec.Register(reg, datagenReferenceShapeOp(f))
	clispec.Register(reg, provisionOp(f))
	registerBatchOps(reg, f)
	reg.AddHandwritten(clispec.HandwrittenCommand{Group: opsGroup, Build: backupCommand})
	reg.AddHandwritten(clispec.HandwrittenCommand{Group: opsGroup, Build: deployCommand})
}

// registerBatchOps renders one leaf under `ops batch` per registered batch op,
// excluding the repair.* helpers that the repair family already exposes. The
// canonical name is used verbatim so dotted op names keep their exact spelling.
func registerBatchOps(reg *clispec.Registry, f *cli.Factory) {
	for _, op := range List() {
		if strings.HasPrefix(op.Name, "repair.") {
			continue
		}
		name := op.Name
		clispec.Register(reg, clispec.Operation[noInput]{
			Name:     clispec.Name{Canonical: name, CLIOverride: name},
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
			Run: func(ctx context.Context, _ noInput, _ clispec.ResultSink) error {
				return Run(ctx, f.Cfg, name)
			},
		})
	}
}
