package ops

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func scopeRepairQuery(orgID uuid.UUID, typeKey string) node.NodeListQuery {
	return node.NodeListQuery{
		OrgID:            orgID,
		NodeType:         typeKey,
		ByProperty:       nil,
		BySourceRelation: nil,
		ByTargetRelation: nil,
		CreatedAfter:     nil,
		CreatedBefore:    nil,
		PropFilters:      nil,
		Limit:            0,
		Cursor:           "",
	}
}
