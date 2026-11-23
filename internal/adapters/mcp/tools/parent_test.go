package tools

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func parentViews(
	views map[uuid.UUID]*node.NodeView,
	parents map[uuid.UUID]uuid.UUID,
	query node.NodeListQuery,
) []*node.NodeView {
	var result []*node.NodeView

	for sourceID, parentID := range parents {
		var targetID uuid.UUID
		if query.BySourceRelation != nil {
			if query.BySourceRelation.RelationType != node.RelChildOf ||
				query.BySourceRelation.NodeID != sourceID {
				continue
			}

			targetID = parentID
		} else {
			if query.ByTargetRelation.RelationType != node.RelChildOf ||
				query.ByTargetRelation.NodeID != parentID {
				continue
			}

			targetID = sourceID
		}

		view := views[targetID]
		if view == nil || (query.NodeType != "" && view.NodeType != query.NodeType) {
			continue
		}

		result = append(result, view)
	}

	return result
}
