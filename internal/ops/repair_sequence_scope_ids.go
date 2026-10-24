package ops

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/domain"
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

// RepairSequenceScopeIDs derives each sequence-bearing node's scope_id from its parent_id
// chain and reclaims each moved node's rendered reference keys. The function counts writes
// that fail with domain.ErrConflict as refused. When the count exceeds zero, the function
// returns an error that wraps domain.ErrConflict and states the refused count.
func RepairSequenceScopeIDs(ctx context.Context, env *Env) (int, int, error) {
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		return 0, 0, err
	}
	totalRepaired, totalSkipped, totalRefused := 0, 0, 0
	for orgID := range orgIDs {
		nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
		if err != nil {
			env.Log.ErrorContext(ctx, "repair.scope_ids: list node types",
				slog.String("org_id", orgID.String()),
				slog.String("err", err.Error()))
			continue
		}
		propertyDefs, err := env.Stores.PropertyDefs.List(ctx, orgID)
		if err != nil {
			env.Log.ErrorContext(ctx, "repair.scope_ids: list property defs",
				slog.String("org_id", orgID.String()),
				slog.String("err", err.Error()))
			continue
		}
		typeIndex := node.BuildTypeIndex(nodeTypes)
		scopeCache := make(map[uuid.UUID]map[string]string)
		for _, nodeType := range nodeTypes {
			if nodeType.Reference.Strategy != node.ReferenceScopedSequence {
				continue
			}
			repaired, skipped, refused := repairTypeScopeIDs(
				ctx, env, orgID, nodeType, propertyDefs, typeIndex, scopeCache,
			)
			totalRepaired += repaired
			totalSkipped += skipped
			totalRefused += refused
		}
	}
	return scopeRepairResult(ctx, env, totalRepaired, totalSkipped, totalRefused)
}

func repairTypeScopeIDs(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	nodeType *node.NodeType,
	propertyDefs []*node.PropertyDef,
	typeIndex map[string]*node.NodeType,
	scopeCache map[uuid.UUID]map[string]string,
) (int, int, int) {
	views, err := env.Stores.Views.List(ctx, node.NodeListQuery{
		OrgID:    orgID,
		NodeType: nodeType.TypeKey,
	})
	if err != nil {
		env.Log.ErrorContext(ctx, "repair.scope_ids: list views",
			slog.String("org_id", orgID.String()),
			slog.String("type", nodeType.TypeKey),
			slog.String("err", err.Error()))
		return 0, 0, 0
	}
	indexedProps := indexedPropsFor(nodeType, propertyDefs)
	repaired, skipped, refused := 0, 0, 0
	for _, view := range views {
		currentNode, readDigest, err := env.Stores.Nodes.GetForUpdate(ctx, orgID, view.ID)
		if err != nil || currentNode == nil {
			skipped++
			continue
		}
		canonicalScopeID := deriveScopeID(ctx, env, currentNode)
		if canonicalScopeID == uuid.Nil {
			skipped++
			continue
		}
		if rawUUIDProp(currentNode.Props, "scope_id") == canonicalScopeID {
			skipped++
			continue
		}

		oldProps := maps.Clone(currentNode.Props)
		newProps := maps.Clone(currentNode.Props)
		scopeRaw, _ := json.Marshal(canonicalScopeID.String())
		newProps["scope_id"] = scopeRaw

		updatedNode := &node.Node{
			ID:        currentNode.ID,
			OrgID:     currentNode.OrgID,
			NodeType:  currentNode.NodeType,
			Name:      currentNode.Name,
			Props:     newProps,
			CreatedBy: currentNode.CreatedBy,
			UpdatedBy: currentNode.UpdatedBy,
			CreatedAt: currentNode.CreatedAt,
			UpdatedAt: currentNode.UpdatedAt,
		}
		updatedView := &node.NodeView{
			ID:        currentNode.ID,
			OrgID:     currentNode.OrgID,
			NodeType:  currentNode.NodeType,
			Name:      currentNode.Name,
			Props:     maps.Clone(newProps),
			CreatedBy: currentNode.CreatedBy,
			UpdatedBy: currentNode.UpdatedBy,
			CreatedAt: currentNode.CreatedAt,
			UpdatedAt: currentNode.UpdatedAt,
		}
		// Moving the scope changes what the node's reference renders to, so
		// claim the keys the new scope produces. Passing none would leave the
		// node holding its old value while nothing holds the new one, which is
		// the silent collision this repair would otherwise create.
		referenceKeys, keyErr := referenceKeysForRepairedView(
			ctx, env, orgID, typeIndex, nodeType, updatedView, scopeCache,
		)
		if keyErr != nil {
			env.Log.WarnContext(ctx, "repair.scope_ids: render reference keys",
				slog.String("node_id", currentNode.ID.String()),
				slog.String("type", nodeType.TypeKey),
				slog.String("err", keyErr.Error()))
			skipped++
			continue
		}
		if err := env.Stores.Nodes.UpdateAtomicFrom(ctx, readDigest, updatedNode, updatedView, oldProps, indexedProps, referenceKeys); err != nil {
			env.Log.WarnContext(ctx, "repair.scope_ids: update node",
				slog.String("node_id", currentNode.ID.String()),
				slog.String("type", nodeType.TypeKey),
				slog.String("err", err.Error()))
			if errors.Is(err, domain.ErrConflict) {
				refused++
			} else {
				skipped++
			}
			continue
		}
		repaired++
	}
	return repaired, skipped, refused
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
		if scopeView, err := env.Stores.Views.Get(ctx, currentScopeID); err == nil && scopeView != nil {
			return currentScopeID
		}
	}
	return uuid.Nil
}
