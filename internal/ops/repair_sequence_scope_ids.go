package ops

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/domain/node"
)

const maxRepairParentDepth = 32

func init() {
	Register(Operation{
		Name:        "repair.sequence_scope_ids",
		Lifetime:    clispec.Permanent,
		Audit:       audit.Spec{Verb: string(audit.VerbOpsRepairApply), Mutates: true},
		Description: "Repair missing or stale scope_id props on sequence-bearing nodes by deriving the owning scope from the parent chain. Idempotent.",
		Run:         runRepairSequenceScopeIDs,
	})
}

func runRepairSequenceScopeIDs(ctx context.Context, env *Env) error {
	_, _, err := RepairSequenceScopeIDs(ctx, env)
	return err
}

// RepairSequenceScopeIDs derives each sequence-bearing node's scope_id from
// its parent_id chain and reclaims each updated node's rendered reference
// keys. The function counts writes that fail with domain.ErrConflict as
// refused. When the count exceeds zero, the function returns an error that
// wraps domain.ErrConflict and states the refused count.
func RepairSequenceScopeIDs(ctx context.Context, env *Env) (int, int, error) {
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		return 0, 0, err
	}

	totalRepaired, totalSkipped, totalRefused := 0, 0, 0

	for orgID := range orgIDs {
		nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
		if err != nil {
			env.Log.ErrorContext(
				ctx,
				"repair.scope_ids: list node types",
				slog.String("org_id", orgID.String()),
				slog.String("err", err.Error()),
			)
			continue
		}

		propertyDefs, err := env.Stores.PropertyDefs.List(ctx, orgID)
		if err != nil {
			env.Log.ErrorContext(
				ctx,
				"repair.scope_ids: list property defs",
				slog.String("org_id", orgID.String()),
				slog.String("err", err.Error()),
			)
			continue
		}

		typeIndex := node.BuildTypeIndex(nodeTypes)
		scopeCache := make(map[uuid.UUID]map[string]string)

		for _, nodeType := range nodeTypes {
			if nodeType.Reference.Strategy != node.ReferenceScopedSequence {
				continue
			}

			repaired, skipped, refused := repairTypeScopeIDs(
				ctx,
				env,
				orgID,
				nodeType,
				propertyDefs,
				typeIndex,
				scopeCache,
			)
			totalRepaired += repaired
			totalSkipped += skipped
			totalRefused += refused
		}
	}

	return scopeRepairResult(ctx, env, totalRepaired, totalSkipped, totalRefused)
}

func deriveScopeID(ctx context.Context, env *Env, currentNode *node.Node) uuid.UUID {
	if currentNode == nil {
		return uuid.Nil
	}

	parentID := rawUUIDProp(currentNode.Props, "parent_id")
	for depth := 0; depth < maxRepairParentDepth && parentID != uuid.Nil; depth++ {
		parentView, err := env.Stores.Views.Get(ctx, parentID)
		if err != nil || parentView == nil {
			return uuid.Nil
		}

		if stringProp(parentView.Props, "identifier") != "" {
			return parentView.ID
		}

		parentID = rawUUIDView(parentView, "parent_id")
	}

	if currentScopeID := rawUUIDProp(currentNode.Props, "scope_id"); currentScopeID != uuid.Nil {
		if scopeView, err := env.Stores.Views.Get(ctx, currentScopeID); err == nil &&
			scopeView != nil {
			return currentScopeID
		}
	}

	return uuid.Nil
}
