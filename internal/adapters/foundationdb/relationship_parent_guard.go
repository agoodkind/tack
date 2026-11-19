package foundationdb

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// refuseOnlyParentRemoval runs inside the relationship removal transaction
// and allows removal of edges of other relation types and child_of edges
// that
// do not exist. When the source node stores parent_id as a UUID string, the
// guard refuses removal of the child_of edge targeting parent_id for every
// node type, even when another child_of edge remains, and allows removal of
// other child_of edges. When the source node stores a parent_id value that
// is
// not a UUID string, the guard refuses removal of every existing child_of
// edge. When the source node does not store parent_id, the guard refuses
// removal if node.HierarchyParent returns an error for the source type and
// the other remaining child_of target, if any. Each refusal returns an error
// that matches domain.ErrFailedPrecondition. FoundationDB aborts one of two
// concurrent transactions that each remove a different parent edge of the
// same node.
func refuseOnlyParentRemoval(
	ctx context.Context,
	transaction fdb.Transaction,
	orgID uuid.UUID,
	sourceID uuid.UUID,
	relationType string,
	targetID uuid.UUID,
) error {
	if relationType != node.RelChildOf {
		return nil
	}

	removedKey := fdb.Key(relationshipKey(orgID, sourceID, relationType, targetID))
	existing, err := transaction.Get(removedKey).Get()
	if err != nil {
		return searchReadFailure(
			ctx,
			"read relationship "+relationType+" of node "+sourceID.String(),
			err,
		)
	}

	if len(existing) == 0 {
		return nil
	}

	sources, err := readHierarchyNodes(
		ctx,
		transaction,
		orgID,
		[]uuid.UUID{sourceID},
		make(map[string]*node.NodeType),
	)
	if err != nil {
		return err
	}

	canonicalID, err := storedParentID(ctx, transaction, orgID, sources[0])
	if err != nil {
		return err
	}

	if canonicalID != uuid.Nil && canonicalID != targetID {
		return nil
	}

	if canonicalID == uuid.Nil {
		otherID, found, err := otherParentTarget(ctx, transaction, orgID, sourceID, removedKey)
		if err != nil {
			return err
		}

		var remaining []uuid.UUID
		if found {
			remaining = []uuid.UUID{otherID}
		}

		if _, parentErr := node.HierarchyParent(
			sourceID,
			sources[0].Kind,
			remaining,
		); parentErr == nil {
			return nil
		}
	}

	wrapped := fmt.Errorf(
		"node %s needs a hierarchy parent, and %s is its only hierarchy parent; move the node to another parent instead: %w",
		sourceID,
		targetID,
		domain.ErrFailedPrecondition,
	)
	telemetry.L(ctx).ErrorContext(
		ctx,
		"relationship.remove_refused",
		slog.String("err", wrapped.Error()),
		slog.String("source_id", sourceID.String()),
		slog.String("target_id", targetID.String()),
	)

	return loggedSearchError{err: wrapped}
}

// parentGuardEdgeLimit limits the guard to reading two child_of edges: the
// edge to remove and one other edge.
const parentGuardEdgeLimit = 2

// otherParentTarget reads at most parentGuardEdgeLimit child_of edges from
// sourceID. It returns the target and true for the first edge with a key
// other
// than removedKey. It returns [uuid.Nil] and false when it does not find
// another
// edge. It returns an error if the range read fails or an edge key does not
// decode.
func otherParentTarget(
	ctx context.Context,
	transaction fdb.Transaction,
	orgID uuid.UUID,
	sourceID uuid.UUID,
	removedKey fdb.Key,
) (uuid.UUID, bool, error) {
	keyRange, err := fdb.PrefixRange(relationshipPrefixBySource(orgID, sourceID, node.RelChildOf))
	if err != nil {
		return uuid.Nil, false, nodeOperationFailure(
			ctx,
			"create relationship range for node "+sourceID.String(),
			err,
		)
	}

	items, err := transaction.GetRange(
		keyRange,
		fdb.RangeOptions{Limit: parentGuardEdgeLimit},
	).GetSliceWithError()
	if err != nil {
		return uuid.Nil, false, searchReadFailure(
			ctx,
			"read child_of edges of node "+sourceID.String(),
			err,
		)
	}

	for _, item := range items {
		if bytes.Equal(item.Key, removedKey) {
			continue
		}

		targetID, err := edgeEnd(ctx, item.Key)
		if err != nil {
			return uuid.Nil, false, err
		}

		return targetID, true, nil
	}

	return uuid.Nil, false, nil
}
