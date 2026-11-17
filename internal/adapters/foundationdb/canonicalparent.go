package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// readGraphParent reads the hierarchy parent from child_of edges inside tr.
// The range read conflicts with concurrent parent relationship changes.
func readGraphParent(
	ctx context.Context,
	tr fdb.Transaction,
	orgID uuid.UUID,
	nodeID uuid.UUID,
) (uuid.UUID, error) {
	edges, err := readOutgoingSnapshots(ctx, tr, orgID, nodeID, node.RelChildOf)
	if err != nil {
		return uuid.Nil, err
	}

	targets := make([]uuid.UUID, 0, len(edges))
	for _, edge := range edges {
		targets = append(targets, edge.Relationship.TargetID)
	}

	parentID, err := node.HierarchyParent(nodeID, nil, targets)
	if err != nil {
		return uuid.Nil, nodeOperationFailure(ctx, "read parent of node "+nodeID.String(), err)
	}

	return parentID, nil
}

// snapshotParent returns the sole distinct child_of target. An ambiguous
// snapshot returns [uuid.Nil]; callers inspect its outgoing edges to request
// an explicit repair.
func snapshotParent(edges []node.RelationshipSnapshot) uuid.UUID {
	parentID := uuid.Nil

	for _, edge := range edges {
		if edge.Relationship.RelationType != node.RelChildOf {
			continue
		}

		if parentID != uuid.Nil && parentID != edge.Relationship.TargetID {
			return uuid.Nil
		}

		parentID = edge.Relationship.TargetID
	}

	return parentID
}
