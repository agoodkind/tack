package ops

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

const maxRepairParentDepth = 32

func init() {
	Register(Operation{
		Name:        "repair.sequence_scope_ids",
		Description: "Repair missing or stale scope_id props on sequence-bearing nodes by deriving the owning scope from the parent chain. Idempotent.",
		Run:         runRepairSequenceScopeIDs,
	})
}

func runRepairSequenceScopeIDs(ctx context.Context, env *Env) error {
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		return err
	}
	totalRepaired, totalSkipped := 0, 0
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
		for _, nodeType := range nodeTypes {
			if nodeType.Reference.Strategy != node.ReferenceScopedSequence {
				continue
			}
			repaired, skipped := repairTypeScopeIDs(ctx, env, orgID, nodeType, propertyDefs)
			totalRepaired += repaired
			totalSkipped += skipped
		}
	}
	env.Log.InfoContext(ctx, "repair.scope_ids.completed",
		slog.Int("repaired", totalRepaired),
		slog.Int("skipped", totalSkipped))
	return nil
}

func repairTypeScopeIDs(ctx context.Context, env *Env, orgID uuid.UUID, nodeType *node.NodeType, propertyDefs []*node.PropertyDef) (int, int) {
	views, err := env.Stores.Views.List(ctx, node.NodeListQuery{
		OrgID:    orgID,
		NodeType: nodeType.TypeKey,
	})
	if err != nil {
		env.Log.ErrorContext(ctx, "repair.scope_ids: list views",
			slog.String("org_id", orgID.String()),
			slog.String("type", nodeType.TypeKey),
			slog.String("err", err.Error()))
		return 0, 0
	}
	indexedProps := indexedPropsFor(nodeType, propertyDefs)
	repaired := 0
	skipped := 0
	for _, view := range views {
		currentNode, err := env.Stores.Nodes.Get(ctx, orgID, view.ID)
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

		oldProps := cloneProps(currentNode.Props)
		newProps := cloneProps(currentNode.Props)
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
			Props:     cloneProps(newProps),
			CreatedBy: currentNode.CreatedBy,
			UpdatedBy: currentNode.UpdatedBy,
			CreatedAt: currentNode.CreatedAt,
			UpdatedAt: currentNode.UpdatedAt,
		}
		if err := env.Stores.Nodes.UpdateAtomic(ctx, updatedNode, updatedView, oldProps, indexedProps); err != nil {
			env.Log.WarnContext(ctx, "repair.scope_ids: update node",
				slog.String("node_id", currentNode.ID.String()),
				slog.String("type", nodeType.TypeKey),
				slog.String("err", err.Error()))
			skipped++
			continue
		}
		repaired++
	}
	return repaired, skipped
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

func cloneProps(input map[string]json.RawMessage) map[string]json.RawMessage {
	if input == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func rawUUIDProp(props map[string]json.RawMessage, key string) uuid.UUID {
	raw, ok := props[key]
	if !ok || len(raw) == 0 {
		return uuid.Nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return uuid.Nil
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func rawUUIDView(view *node.NodeView, key string) uuid.UUID {
	if view == nil {
		return uuid.Nil
	}
	return rawUUIDProp(view.Props, key)
}

func stringProp(props map[string]json.RawMessage, key string) string {
	raw, ok := props[key]
	if !ok || len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}
