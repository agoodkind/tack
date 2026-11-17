package foundationdb

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// scopeIDProperty is the property that stores the ID of a node's reference
// and numbering scope.
const scopeIDProperty = "scope_id"

// checkMoveScope returns an error that wraps domain.ErrInvalidArgument when
// plan changes a stored node's parent and the parent chain in records does
// not contain the scope_id of result. It returns nil when result does not
// have a scope_id or plan keeps the stored parent.
func checkMoveScope(
	ctx context.Context,
	plan node.MutationPlan,
	source mutationSource,
	result node.Node,
	records map[uuid.UUID]mutationSource,
) error {
	if plan.Parent == nil || !source.Exists {
		return nil
	}

	parentID := plan.Parent.TargetID
	scopeID := storedUUID(result.Props[scopeIDProperty])
	if scopeID == uuid.Nil || parentID == storedUUID(source.Node.Props[parentIDProperty]) {
		return nil
	}

	for currentID := parentID; currentID != uuid.Nil; {
		if currentID == scopeID {
			return nil
		}

		ancestor, listed := records[currentID]
		if !listed {
			break
		}

		currentID = storedUUID(ancestor.Node.Props[parentIDProperty])
	}

	return mutationRefusal(
		ctx,
		plan.NodeID,
		"the parent chain of "+parentID.String()+" does not contain scope "+scopeID.String(),
		domain.ErrInvalidArgument,
	)
}

// mutationRefusal logs a refused mutation of nodeID with reason and returns
// an
// error that wraps sentinel.
func mutationRefusal(ctx context.Context, nodeID uuid.UUID, reason string, sentinel error) error {
	wrapped := fmt.Errorf("mutate node %s: %s: %w", nodeID, reason, sentinel)
	slog.WarnContext(
		ctx,
		"node.mutation_refused",
		slog.String("err", wrapped.Error()),
		slog.String("node_id", nodeID.String()),
	)

	return loggedSearchError{err: wrapped}
}
