package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func checkLegacyParentChain(
	ctx context.Context,
	transaction fdb.Transaction,
	current *node.Node,
	parentID uuid.UUID,
) error {
	if err := checkLegacyParentType(ctx, transaction, current, parentID); err != nil {
		return err
	}

	scopeID := storedUUID(current.Props[scopeIDProperty])
	if _, present := current.Props[scopeIDProperty]; present && scopeID == uuid.Nil {
		return mutationRefusal(
			ctx,
			current.ID,
			"scope_id must contain a nonzero node UUID; repair the scope before migrating",
			domain.ErrInvalidArgument,
		)
	}

	scopeFound := scopeID == uuid.Nil
	visited := make(map[uuid.UUID]bool)

	for currentID := parentID; currentID != uuid.Nil; {
		if currentID == current.ID || visited[currentID] {
			return mutationRefusal(
				ctx,
				current.ID,
				"the legacy parent would create a child_of cycle",
				domain.ErrInvalidArgument,
			)
		}

		visited[currentID] = true
		ancestor, err := readLegacyAncestor(ctx, transaction, current, currentID)
		if err != nil {
			return err
		}

		if currentID == scopeID {
			scopeFound = true
		}

		currentID, err = effectiveLegacyParent(ctx, transaction, ancestor)
		if err != nil {
			return err
		}
	}

	if !scopeFound {
		return mutationRefusal(
			ctx,
			current.ID,
			"the proposed parent chain does not contain scope "+scopeID.String()+"; repair the parent before migrating",
			domain.ErrInvalidArgument,
		)
	}

	return nil
}

func readLegacyAncestor(
	ctx context.Context,
	transaction fdb.Transaction,
	current *node.Node,
	ancestorID uuid.UUID,
) (*node.Node, error) {
	source, err := readMutationSource(ctx, transaction, current.OrgID, ancestorID)
	if err != nil {
		return nil, err
	}

	if !source.Exists {
		return nil, mutationRefusal(
			ctx,
			current.ID,
			"parent "+ancestorID.String()+" is not a node in the organization",
			domain.ErrInvalidArgument,
		)
	}

	return &source.Node, nil
}

func effectiveLegacyParent(
	ctx context.Context,
	transaction fdb.Transaction,
	ancestor *node.Node,
) (uuid.UUID, error) {
	edges, err := readOutgoingSnapshots(
		ctx,
		transaction,
		ancestor.OrgID,
		ancestor.ID,
		node.RelChildOf,
	)
	if err != nil {
		return uuid.Nil, err
	}

	if len(edges) == 0 {
		return legacyParentID(ctx, ancestor)
	}

	targets := make([]uuid.UUID, 0, len(edges))
	for _, edge := range edges {
		targets = append(targets, edge.Relationship.TargetID)
	}

	parentID, err := node.HierarchyParent(ancestor.ID, nil, targets)
	if err != nil {
		return uuid.Nil, nodeOperationFailure(
			ctx,
			"check the parent of ancestor "+ancestor.ID.String(),
			err,
		)
	}

	return parentID, nil
}
