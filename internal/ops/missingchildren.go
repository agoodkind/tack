package ops

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func defaultChildrenQuery(
	orgID uuid.UUID,
	typeKey string,
	parent *node.RelationScan,
) node.NodeListQuery {
	return node.NodeListQuery{
		OrgID:            orgID,
		NodeType:         typeKey,
		ByProperty:       nil,
		BySourceRelation: nil,
		ByTargetRelation: parent,
		CreatedAfter:     nil,
		CreatedBefore:    nil,
		PropFilters:      nil,
		Limit:            0,
		Cursor:           "",
	}
}
