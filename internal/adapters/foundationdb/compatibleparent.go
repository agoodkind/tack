package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func checkLegacyParentType(
	ctx context.Context,
	transaction fdb.Transaction,
	current *node.Node,
	parentID uuid.UUID,
) error {
	parent, err := readLegacyAncestor(ctx, transaction, current, parentID)
	if err != nil {
		return err
	}

	kinds := make(map[string]*node.NodeType)
	childKind, err := cachedNodeTypeByKey(ctx, transaction, current.OrgID, current.NodeType, kinds)
	if err != nil {
		return err
	}

	if childKind == nil {
		return mutationRefusal(
			ctx,
			current.ID,
			"the node type is missing; repair metadata before migrating parents",
			domain.ErrInvalidArgument,
		)
	}

	parentKind, err := cachedNodeTypeByKey(ctx, transaction, current.OrgID, parent.NodeType, kinds)
	if err != nil {
		return err
	}

	if !node.LivesUnder(childKind, parentKind) {
		return mutationRefusal(
			ctx,
			current.ID,
			"the node type cannot use parent "+parentID.String()+"; repair the parent before migrating",
			domain.ErrInvalidArgument,
		)
	}

	return nil
}
