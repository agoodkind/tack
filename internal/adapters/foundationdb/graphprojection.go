package foundationdb

import (
	"maps"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// planRelationshipChanges returns the relationship changes from plan and
// sets
// the organization and source of each added relationship if they are empty.
func planRelationshipChanges(plan node.MutationPlan) node.RelationshipChanges {
	added := make([]*node.Relationship, 0, len(plan.Relationships.Add))
	for _, relationship := range plan.Relationships.Add {
		stamped := *relationship
		if stamped.OrgID == uuid.Nil {
			stamped.OrgID = plan.OrgID
		}

		if stamped.SourceID == uuid.Nil {
			stamped.SourceID = plan.NodeID
		}

		added = append(added, &stamped)
	}

	return node.RelationshipChanges{Add: added, Remove: plan.Relationships.Remove}
}

// mutationView returns the view record for result.
func mutationView(result *node.Node) *node.NodeView {
	return &node.NodeView{
		ID:        result.ID,
		OrgID:     result.OrgID,
		NodeType:  result.NodeType,
		Name:      result.Name,
		Props:     maps.Clone(result.Props),
		CreatedBy: result.CreatedBy,
		UpdatedBy: result.UpdatedBy,
		CreatedAt: result.CreatedAt,
		UpdatedAt: result.UpdatedAt,
	}
}
