package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
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
		parent, err := repairParentView(ctx, env.Stores.Views, orgID, view.ID)
		if err != nil {
			return nil, err
		}

		if parent != nil {
			startID = parent.ID
		}
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
			env.Log.WarnContext(
				ctx,
				"reference.duplicates.scope_get_failed",
				slog.String("org_id", orgID.String()),
				slog.String("node_id", currentID.String()),
				slog.String("err", wrapped.Error()),
			)

			return nil, wrapped
		}

		if ancestor == nil {
			break
		}

		addAncestorReferences(references, typeIndex[ancestor.NodeType], ancestor)

		parent, err := repairParentView(ctx, env.Stores.Views, orgID, ancestor.ID)
		if err != nil {
			return nil, err
		}

		currentID = uuid.Nil
		if parent != nil {
			currentID = parent.ID
		}
	}

	if currentID != uuid.Nil {
		err := fmt.Errorf(
			"scope walk from %s exceeded %d ancestors: %w",
			view.ID,
			maxRepairParentDepth,
			domain.ErrFailedPrecondition,
		)
		env.Log.WarnContext(
			ctx,
			"reference.duplicates.scope_depth_exceeded",
			slog.String("node_id", view.ID.String()),
			slog.String("err", err.Error()),
		)

		return nil, err
	}

	cache[startID] = references

	return references, nil
}

func addAncestorReferences(
	references map[string]string,
	ancestorType *node.NodeType,
	ancestor *node.NodeView,
) {
	if ancestorType == nil {
		return
	}

	propertyName := ancestorType.Reference.DirectAddressProperty()
	reference := stringProp(ancestor.Props, propertyName)
	if propertyName == "" || reference == "" {
		return
	}

	for _, feature := range ancestorType.Features {
		if references[feature] == "" {
			references[feature] = reference
		}
	}
}
