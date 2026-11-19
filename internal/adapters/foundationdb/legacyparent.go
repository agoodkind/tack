package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func (s *NodeStore) legacyParentRelationship(
	ctx context.Context,
	transaction fdb.Transaction,
	current *node.Node,
) (*node.Relationship, error) {
	edges, err := readOutgoingSnapshots(
		ctx,
		transaction,
		current.OrgID,
		current.ID,
		node.RelChildOf,
	)
	if err != nil || len(edges) > 0 {
		return nil, err
	}

	parentID, err := legacyParentID(ctx, current)
	if err != nil || parentID == uuid.Nil {
		return nil, err
	}

	if err := checkLegacyParentChain(ctx, transaction, current, parentID); err != nil {
		return nil, err
	}

	return &node.Relationship{
		OrgID:        current.OrgID,
		SourceID:     current.ID,
		RelationType: node.RelChildOf,
		TargetID:     parentID,
		CreatedBy:    current.CreatedBy,
		CreatedAt:    s.clock.Now().UTC(),
		Props:        nil,
	}, nil
}
