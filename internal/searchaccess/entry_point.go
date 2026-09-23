package searchaccess

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// entryPoint walks from resourceID through hierarchy parents until it reads
// a node with an entry-point type. Each level reads one node, one node type
// by key, and bounded pages of the node's edges. NodeType metadata defines
// the hierarchy. The walk has no depth limit and rejects a cycle.
func (c *OrgScopeCompiler) entryPoint(ctx context.Context, orgID, resourceID uuid.UUID) (uuid.UUID, error) {
	if c.reader == nil || c.types == nil || c.relationships == nil {
		return uuid.Nil, entryPointFailure(ctx, resourceID, "resolve entry point", fmt.Errorf("search policy dependencies are unavailable"))
	}
	visited := make(map[uuid.UUID]struct{})
	currentID := resourceID
	for {
		if _, seen := visited[currentID]; seen {
			return uuid.Nil, entryPointFailure(ctx, resourceID, "resolve entry point", fmt.Errorf("hierarchy cycle at %s", currentID))
		}
		visited[currentID] = struct{}{}
		view, err := c.reader.Get(ctx, currentID)
		if err != nil {
			return uuid.Nil, entryPointFailure(ctx, resourceID, "get search ancestor "+currentID.String(), err)
		}
		if view == nil || view.OrgID != orgID {
			return uuid.Nil, entryPointFailure(ctx, resourceID, "resolve entry point", fmt.Errorf("ancestor %s is missing or belongs to another organization", currentID))
		}
		kind, err := c.types.TypeByKey(ctx, orgID, view.NodeType)
		if err != nil {
			return uuid.Nil, entryPointFailure(ctx, resourceID, "read node type "+view.NodeType, err)
		}
		if kind == nil {
			return uuid.Nil, entryPointFailure(ctx, resourceID, "resolve entry point", fmt.Errorf("node type %q is missing", view.NodeType))
		}
		if kind.Features.Has(node.FeatureIsEntryPoint) {
			return currentID, nil
		}
		parentID, err := c.parent(ctx, orgID, currentID, kind)
		if err != nil {
			return uuid.Nil, WithContext("resolve hierarchy parent of ancestor "+currentID.String(), err)
		}
		currentID = parentID
	}
}

func entryPointFailure(ctx context.Context, resourceID uuid.UUID, operation string, err error) error {
	wrapped := fmt.Errorf("%s for resource %s: %w", operation, resourceID, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.access.entry_point_failed",
		slog.String("err", wrapped.Error()), slog.String("resource_id", resourceID.String()))
	return loggedAccessError{err: wrapped}
}
