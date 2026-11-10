package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// scopeReferencesForView resolves the scope references for one node, caching
// by the walk's starting scope so sibling nodes in one scope share a single
// ancestor walk instead of repeating it per node.
func scopeReferencesForView(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	typeIndex map[string]*node.NodeType,
	view *node.NodeView,
	cache map[uuid.UUID]map[string]string,
) (map[string]string, error) {
	startID := rawUUIDProp(view.Props, "scope_id")
	if startID == uuid.Nil {
		startID = rawUUIDProp(view.Props, "parent_id")
	}
	if cached, ok := cache[startID]; ok {
		return cached, nil
	}
	references := make(map[string]string)
	currentID := startID
	for depth := 0; currentID != uuid.Nil && depth < maxRepairParentDepth; depth++ {
		ancestor, err := env.Stores.Views.Get(ctx, currentID)
		if err != nil {
			wrapped := fmt.Errorf("get scope ancestor %s for org %s: %w", currentID, orgID, err)
			env.Log.WarnContext(ctx, "reference.duplicates.scope_get_failed",
				slog.String("org_id", orgID.String()),
				slog.String("node_id", currentID.String()),
				slog.String("err", wrapped.Error()),
			)
			return nil, wrapped
		}
		if ancestor == nil {
			break
		}
		ancestorType := typeIndex[ancestor.NodeType]
		if ancestorType != nil {
			propertyName := ancestorType.Reference.DirectAddressProperty()
			reference := stringProp(ancestor.Props, propertyName)
			if propertyName != "" && reference != "" {
				for _, feature := range ancestorType.Features {
					if references[feature] == "" {
						references[feature] = reference
					}
				}
			}
		}
		currentID = rawUUIDView(ancestor, "parent_id")
	}
	if currentID != uuid.Nil {
		err := fmt.Errorf("scope walk from %s exceeded %d ancestors", view.ID, maxRepairParentDepth)
		env.Log.WarnContext(ctx, "reference.duplicates.scope_depth_exceeded",
			slog.String("node_id", view.ID.String()),
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	cache[startID] = references
	return references, nil
}
