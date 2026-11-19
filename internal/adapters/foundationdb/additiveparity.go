package foundationdb

import (
	"bytes"
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// checkAdditiveParentParity refuses conflicting stored parents.
func checkAdditiveParentParity(
	ctx context.Context,
	transaction fdb.Transaction,
	current *node.Node,
	view *node.NodeView,
) error {
	if view == nil || !bytes.Equal(
		current.Props[legacyParentProperty],
		view.Props[legacyParentProperty],
	) {
		return mutationRefusal(
			ctx,
			current.ID,
			"the primary and read view parents disagree; repair the records before migrating",
			domain.ErrInvalidArgument,
		)
	}

	parentID, err := legacyParentID(ctx, current)
	if err != nil {
		return err
	}

	edges, err := readOutgoingSnapshots(
		ctx,
		transaction,
		current.OrgID,
		current.ID,
		node.RelChildOf,
	)
	if err != nil {
		return err
	}

	if len(edges) > 1 {
		return mutationRefusal(
			ctx,
			current.ID,
			"the node has ambiguous child_of relationships; repair the parents before migrating",
			domain.ErrInvalidArgument,
		)
	}

	if len(edges) == 1 && edges[0].Relationship.TargetID != parentID {
		return mutationRefusal(
			ctx,
			current.ID,
			"the child_of relationship and parent_id disagree; repair the parents before migrating",
			domain.ErrInvalidArgument,
		)
	}

	if parentID != uuid.Nil {
		return checkLegacyParentChain(ctx, transaction, current, parentID)
	}

	return nil
}
