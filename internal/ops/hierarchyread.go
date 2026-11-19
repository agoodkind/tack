package ops

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func repairParentView(
	ctx context.Context,
	reader node.NodeReader,
	orgID uuid.UUID,
	nodeID uuid.UUID,
) (*node.NodeView, error) {
	parents, err := reader.List(ctx, repairParentQuery(orgID, nodeID))
	if err != nil {
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf("read child_of parent of node %s", nodeID),
			err,
		)
	}

	if len(parents) > 1 {
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf("node %s has %d child_of parents", nodeID, len(parents)),
			domain.ErrConflict,
		)
	}

	if len(parents) == 0 {
		return nil, nil
	}

	return parents[0], nil
}

func repairParentQuery(orgID, nodeID uuid.UUID) node.NodeListQuery {
	return node.NodeListQuery{
		OrgID:            orgID,
		NodeType:         "",
		ByProperty:       nil,
		BySourceRelation: &node.RelationScan{NodeID: nodeID, RelationType: node.RelChildOf},
		ByTargetRelation: nil,
		CreatedAfter:     nil,
		CreatedBefore:    nil,
		PropFilters:      nil,
		Limit:            0,
		Cursor:           "",
	}
}
