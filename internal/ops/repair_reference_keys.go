package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func writeAllReferenceKeys(ctx context.Context, env *Env, orgID uuid.UUID) (int, error) {
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list node types for org %s: %w", orgID, err)
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.types_failed",
			slog.String("org_id", orgID.String()), slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	typeIndex := node.BuildTypeIndex(nodeTypes)
	written := 0
	scopeCache := make(map[uuid.UUID]map[string]string)
	for _, nodeType := range nodeTypes {
		if len(nodeType.ReferenceTemplates) == 0 {
			continue
		}
		views, listErr := env.Stores.Views.List(ctx, node.NodeListQuery{
			OrgID:            orgID,
			NodeType:         nodeType.TypeKey,
			ByProperty:       nil,
			BySourceRelation: nil,
			ByTargetRelation: nil,
			CreatedAfter:     nil,
			CreatedBefore:    nil,
			PropFilters:      nil,
			Limit:            0,
			Cursor:           "",
		})
		if listErr != nil {
			wrapped := fmt.Errorf("list %s nodes in org %s: %w", nodeType.TypeKey, orgID, listErr)
			env.Log.WarnContext(ctx, "repair.reference_uniqueness.views_failed",
				slog.String("org_id", orgID.String()),
				slog.String("node_type", nodeType.TypeKey),
				slog.String("err", wrapped.Error()),
			)
			return 0, wrapped
		}
		for _, view := range views {
			keys, keyErr := referenceKeysForRepairedView(ctx, env, orgID, typeIndex, nodeType, view, scopeCache)
			if keyErr != nil {
				return 0, keyErr
			}
			if len(keys) == 0 {
				continue
			}
			if writeErr := env.Stores.Nodes.SetReferenceKeys(ctx, orgID, view.ID, keys); writeErr != nil {
				wrapped := fmt.Errorf("write reference keys for node %s: %w", view.ID, writeErr)
				env.Log.WarnContext(ctx, "repair.reference_uniqueness.keys_write_failed",
					slog.String("node_id", view.ID.String()), slog.String("err", wrapped.Error()))
				return 0, wrapped
			}
			written += len(keys)
		}
	}
	return written, nil
}

func referenceKeysForRepairedView(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	typeIndex map[string]*node.NodeType,
	nodeType *node.NodeType,
	view *node.NodeView,
	scopeCache map[uuid.UUID]map[string]string,
) ([]node.ReferenceKey, error) {
	scopeRefs, err := scopeReferencesForView(ctx, env, orgID, typeIndex, view, scopeCache)
	if err != nil {
		return nil, err
	}
	input := node.ReferenceRenderInput{
		NodeTypeKey: nodeType.TypeKey,
		Props:       view.Props,
		ScopeRefs:   scopeRefs,
	}
	keys := make([]node.ReferenceKey, 0, len(nodeType.ReferenceTemplates))
	for _, template := range nodeType.ReferenceTemplates {
		encoded, renderErr := template.Render(input)
		if renderErr != nil {
			wrapped := fmt.Errorf("render template %q for node %s: %w", template.Name, view.ID, renderErr)
			env.Log.WarnContext(ctx, "repair.reference_uniqueness.render_failed",
				slog.String("node_id", view.ID.String()), slog.String("err", wrapped.Error()))
			return nil, wrapped
		}
		keys = append(keys, node.ReferenceKey{TemplateName: template.Name, Encoded: encoded})
	}
	return keys, nil
}
