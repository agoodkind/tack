package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/searchaccess"
)

func (s *NodeSummaryStore) summaryAccesses(
	ctx context.Context,
	transaction fdb.Transaction,
	nodeIDs []uuid.UUID,
	views []*node.NodeView,
) (map[searchaccess.OrgNode]searchaccess.ResourceAccess, error) {
	resources := make([]searchaccess.OrgNode, 0, len(nodeIDs))
	for position, nodeID := range nodeIDs {
		if views[position] != nil {
			resources = append(
				resources,
				searchaccess.OrgNode{OrgID: views[position].OrgID, NodeID: nodeID},
			)
		}
	}

	accesses, err := s.policies.BatchResourceKeys(ctx, newSummaryHierarchy(transaction), resources)
	if err != nil {
		return nil, searchReadFailure(ctx, "compile current access of the summary batch", err)
	}

	return accesses, nil
}
