package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func (s *NodeService) scopeParentID(
	ctx context.Context,
	orgID uuid.UUID,
	nodeID uuid.UUID,
) (uuid.UUID, error) {
	parents, err := s.reader.List(
		ctx,
		node.NodeListQuery{
			OrgID:            orgID,
			NodeType:         "",
			ByProperty:       nil,
			ByTargetRelation: nil,
			BySourceRelation: &node.RelationScan{NodeID: nodeID, RelationType: node.RelChildOf},
			CreatedAfter:     nil,
			CreatedBefore:    nil,
			PropFilters:      nil,
			Limit:            0,
			Cursor:           "",
		},
	)
	if err != nil {
		slog.ErrorContext(ctx, "node.reference.parent_read_failed", slog.String("err", err.Error()))

		return uuid.Nil, fmt.Errorf("read parent of scope node %s: %w", nodeID, err)
	}

	if len(parents) > 1 {
		err := fmt.Errorf("scope node %s %w", nodeID, errScopeMultipleParents)
		slog.WarnContext(ctx, "node.reference.parents_ambiguous", slog.String("err", err.Error()))

		return uuid.Nil, err
	}

	if len(parents) == 1 {
		return parents[0].ID, nil
	}

	return uuid.Nil, nil
}
