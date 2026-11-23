package ops

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// repairParentChange returns the resulting parent when the result sets
// its parent instruction or removes a child_of edge. A result that only
// removes edges keeps the current parent.
func repairParentChange(
	ctx context.Context,
	entry NodeIntegrityEntry,
	snapshot node.MutationSnapshot,
) (*node.ParentChange, error) {
	if entry.Result.ParentID != nil {
		parentID := resultParent(entry.Result)
		if parentID == uuid.Nil {
			return nil, repairRefused(
				ctx,
				entry,
				"the result parent_id is not a node ID",
				domain.ErrInvalidArgument,
			)
		}

		return &node.ParentChange{TargetID: parentID}, nil
	}

	for _, edge := range entry.Result.RemoveEdges {
		if edge.RelationType != node.RelChildOf {
			continue
		}

		if snapshot.ParentID == uuid.Nil {
			return nil, repairRefused(
				ctx,
				entry,
				"the node does not have a parent to keep",
				domain.ErrConflict,
			)
		}

		return &node.ParentChange{TargetID: snapshot.ParentID}, nil
	}

	return nil, nil
}

// removedEdgeExpectations returns the stored digests of the outgoing edges
// that the mutation removes. When parent has a value, the mutation removes
// every child_of edge that does not target the parent.
// removedEdgeExpectations refuses a removal that the manifest does not
// list. It also refuses a listed edge that the node does not have.
func removedEdgeExpectations(
	ctx context.Context,
	entry NodeIntegrityEntry,
	snapshot node.MutationSnapshot,
	parent *node.ParentChange,
) ([]node.RelationshipExpectation, error) {
	listed := make(map[RepairEdge]bool, len(entry.Result.RemoveEdges))
	for _, edge := range entry.Result.RemoveEdges {
		listed[edge] = true
	}

	expectations := []node.RelationshipExpectation{}

	for _, stored := range snapshot.Outgoing {
		edge := RepairEdge{
			RelationType: stored.Relationship.RelationType,
			TargetID:     stored.Relationship.TargetID,
		}
		derived := parent != nil && edge.RelationType == node.RelChildOf &&
			edge.TargetID != parent.TargetID
		if derived && !listed[edge] {
			return nil, repairRefused(
				ctx,
				entry,
				fmt.Sprintf(
					"the new parent removes the child_of edge to %s, which the manifest does not list",
					edge.TargetID,
				),
				domain.ErrConflict,
			)
		}

		if !listed[edge] {
			continue
		}

		if edge.RelationType == node.RelChildOf && !derived {
			return nil, repairRefused(
				ctx,
				entry,
				fmt.Sprintf(
					"the manifest removes the child_of edge to the resulting parent %s",
					edge.TargetID,
				),
				domain.ErrConflict,
			)
		}

		delete(listed, edge)
		expectations = append(
			expectations,
			node.RelationshipExpectation{
				RelationType: edge.RelationType,
				TargetID:     edge.TargetID,
				Digest:       stored.Digest,
			},
		)
	}

	for _, edge := range entry.Result.RemoveEdges {
		if listed[edge] {
			return nil, repairRefused(
				ctx,
				entry,
				fmt.Sprintf(
					"the node does not have a %s edge to %s",
					edge.RelationType,
					edge.TargetID,
				),
				domain.ErrConflict,
			)
		}
	}

	return expectations, nil
}
