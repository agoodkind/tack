package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func seedReferenceCounters(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	execute bool,
) (int, error) {
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list node types for org %s: %w", orgID, err)
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.types_failed",
			slog.String("org_id", orgID.String()), slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	typeIndex := node.BuildTypeIndex(nodeTypes)
	highest := make(map[string]int64)
	scopeCache := make(map[uuid.UUID]map[string]string)
	for _, nodeType := range nodeTypes {
		template := nodeType.PrimaryReferenceTemplate()
		if template == nil || template.Generated == "" {
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
			if recordErr := recordHighestGenerated(
				ctx, env, orgID, typeIndex, nodeType, *template, view, highest, scopeCache,
			); recordErr != nil {
				return 0, recordErr
			}
		}
	}
	if !execute {
		return len(highest), nil
	}
	for counterKey, value := range highest {
		if seedErr := env.Stores.Nodes.SeedSequenceByKey(ctx, orgID, counterKey, value); seedErr != nil {
			wrapped := fmt.Errorf("seed counter %q in org %s: %w", counterKey, orgID, seedErr)
			env.Log.WarnContext(ctx, "repair.reference_uniqueness.counter_seed_failed",
				slog.String("org_id", orgID.String()),
				slog.String("counter_key", counterKey),
				slog.String("err", wrapped.Error()),
			)
			return 0, wrapped
		}
	}
	return len(highest), nil
}

func recordHighestGenerated(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	typeIndex map[string]*node.NodeType,
	nodeType *node.NodeType,
	template node.ReferenceTemplate,
	view *node.NodeView,
	highest map[string]int64,
	scopeCache map[uuid.UUID]map[string]string,
) error {
	scopeRefs, err := scopeReferencesForView(ctx, env, orgID, typeIndex, view, scopeCache)
	if err != nil {
		return err
	}
	counterKey, err := template.CounterKey(node.ReferenceRenderInput{
		NodeTypeKey: nodeType.TypeKey,
		Props:       view.Props,
		ScopeRefs:   scopeRefs,
	})
	if err != nil {
		wrapped := fmt.Errorf("derive counter key for node %s: %w", view.ID, err)
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.counter_key_failed",
			slog.String("node_id", view.ID.String()), slog.String("err", wrapped.Error()))
		return wrapped
	}
	value := numberPropValue(view.Props, template.Generated)
	if value > highest[counterKey] {
		highest[counterKey] = value
	}
	return nil
}

func numberPropValue(props map[string]json.RawMessage, name string) int64 {
	raw := props[name]
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0
	}
	return value
}
