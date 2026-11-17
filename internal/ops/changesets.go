package ops

import (
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

// repairRelationshipChanges returns the changes to edges other than
// child_of. The mutation derives child_of edges from the resulting parent.
func repairRelationshipChanges(
	entry NodeIntegrityEntry,
	frozen repairFrozen,
	operator audit.OperatorPrincipal,
) node.RelationshipChanges {
	changes := node.RelationshipChanges{Add: nil, Remove: nil}
	now := clock.Now().UTC()

	for _, edge := range entry.Result.AddEdges {
		changes.Add = append(
			changes.Add,
			&node.Relationship{
				OrgID:        frozen.orgID,
				SourceID:     entry.NodeID,
				RelationType: edge.RelationType,
				TargetID:     edge.TargetID,
				CreatedBy:    operator.ID,
				CreatedAt:    now,
				Props:        nil,
			},
		)
	}

	for _, edge := range entry.Result.RemoveEdges {
		if edge.RelationType == node.RelChildOf {
			continue
		}

		changes.Remove = append(
			changes.Remove,
			&node.Relationship{
				OrgID:        frozen.orgID,
				SourceID:     entry.NodeID,
				RelationType: edge.RelationType,
				TargetID:     edge.TargetID,
				CreatedBy:    operator.ID,
				CreatedAt:    now,
				Props:        nil,
			},
		)
	}

	return changes
}
