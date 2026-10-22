package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

// ParentEdgeRemover removes one relationship in one transaction. The
// transaction writes the change event of the removed relationship.
type ParentEdgeRemover interface {
	Remove(ctx context.Context, orgID, sourceID uuid.UUID, relationType string, targetID uuid.UUID) error
}

// removeExtraParentEdges removes each child_of edge in edges and returns the
// count of removed edges. Each removal writes one relationship.remove change
// event under the audit operation of ctx, in the removal transaction.
func removeExtraParentEdges(ctx context.Context, remover ParentEdgeRemover, edges []fdbadapter.ParentEdge) (int, error) {
	removed := 0
	for _, edge := range edges {
		if err := remover.Remove(ctx, edge.OrgID, edge.NodeID, node.RelChildOf, edge.TargetID); err != nil {
			wrapped := fmt.Errorf("remove child_of edge from %s to %s: %w", edge.NodeID, edge.TargetID, err)
			slog.ErrorContext(ctx, "orphan_nodes.edge_remove_failed", slog.String("err", wrapped.Error()))
			return removed, wrapped
		}
		removed++
	}
	return removed, nil
}
