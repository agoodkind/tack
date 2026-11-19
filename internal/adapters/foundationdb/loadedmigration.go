package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

type parentMigration struct {
	current      *node.Node
	relationship *node.Relationship
}

func (s *NodeStore) readParentMigration(
	ctx context.Context,
	transaction fdb.Transaction,
	orgID uuid.UUID,
	nodeID uuid.UUID,
	nodeType string,
) (*parentMigration, error) {
	source, err := readMutationSource(ctx, transaction, orgID, nodeID)
	if err != nil {
		return nil, err
	}

	if !source.Exists || source.TypeKey != nodeType || source.Node.ID != nodeID ||
		source.Node.OrgID != orgID {
		return nil, mutationRefusal(
			ctx,
			nodeID,
			"the primary record and resolution disagree; repair node resolution before migrating parents",
			domain.ErrInvalidArgument,
		)
	}

	current, view, _, err := readNodeAndView(
		ctx,
		transaction,
		orgID,
		hierarchyNode{ID: nodeID, TypeKey: source.TypeKey, Kind: nil, Found: true},
	)
	if err != nil {
		return nil, err
	}

	if err := checkAdditiveParentParity(ctx, transaction, current, view); err != nil {
		return nil, err
	}

	relationship, err := s.legacyParentRelationship(ctx, transaction, current)
	if err != nil {
		return nil, err
	}

	if relationship == nil {
		return nil, nil
	}

	return &parentMigration{
		current:      current,
		relationship: relationship,
	}, nil
}
