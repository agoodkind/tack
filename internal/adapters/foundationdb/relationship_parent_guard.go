package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// refuseOnlyParentRemoval refuses the removal of the child_of edge from
// sourceID to targetID when that edge is the source node's only child_of
// edge and the source type lists CanLiveUnder types. Search access
// compilation applies the same rule through [node.HierarchyParent], whatever
// the types of the two nodes are. The removal of an edge of any other
// relation type passes. The guard reads the source's child_of edges in the
// removal transaction. FoundationDB aborts one of two concurrent
// transactions that each remove a different parent edge of the same node.
func refuseOnlyParentRemoval(ctx context.Context, tr fdb.Transaction, orgID, sourceID uuid.UUID, relationType string, targetID uuid.UUID) error {
	if relationType != node.RelChildOf {
		return nil
	}
	removedKey := fdb.Key(relationshipKey(orgID, sourceID, relationType, targetID))
	existing, err := tr.Get(removedKey).Get()
	if err != nil {
		return searchReadFailure(ctx, "read relationship "+relationType+" of node "+sourceID.String(), err)
	}
	if len(existing) == 0 {
		return nil
	}
	sources, err := readHierarchyNodes(ctx, tr, orgID, []uuid.UUID{sourceID}, make(map[string]*node.NodeType))
	if err != nil {
		return err
	}
	remaining, err := otherParentTarget(ctx, tr, orgID, sourceID, removedKey)
	if err != nil {
		return err
	}
	_, parentErr := node.HierarchyParent(sourceID, sources[0].Kind, remaining)
	if parentErr == nil {
		return nil
	}
	wrapped := fmt.Errorf("node %s needs a hierarchy parent, and %s is its only hierarchy parent; move the node to another parent instead: %w",
		sourceID, targetID, domain.ErrFailedPrecondition)
	telemetry.L(ctx).ErrorContext(ctx, "relationship.remove_refused", slog.String("err", wrapped.Error()),
		slog.String("source_id", sourceID.String()), slog.String("target_id", targetID.String()))
	return loggedSearchError{err: wrapped}
}

// parentGuardEdgeLimit limits the guard to reading two child_of edges: the
// edge to remove and one other edge.
const parentGuardEdgeLimit = 2

// otherParentTarget reads at most parentGuardEdgeLimit child_of edges from
// sourceID and returns the target of the first edge with a key other than
// removedKey, if one exists.
func otherParentTarget(ctx context.Context, tr fdb.Transaction, orgID, sourceID uuid.UUID, removedKey fdb.Key) ([]uuid.UUID, error) {
	keyRange, err := fdb.PrefixRange(relationshipPrefixBySource(orgID, sourceID, node.RelChildOf))
	if err != nil {
		return nil, nodeOperationFailure(ctx, "create relationship range for node "+sourceID.String(), err)
	}
	items, err := tr.GetRange(keyRange, fdb.RangeOptions{Limit: parentGuardEdgeLimit}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read child_of edges of node "+sourceID.String(), err)
	}
	for _, item := range items {
		if bytes.Equal(item.Key, removedKey) {
			continue
		}
		targetID, err := edgeEnd(ctx, item.Key)
		if err != nil {
			return nil, err
		}
		return []uuid.UUID{targetID}, nil
	}
	return nil, nil
}

// cachedNodeTypeByKey reads the node type of orgID that uses typeKey through
// the type-key index. A missing type yields nil, and a type key that several
// node types use is an error.
func cachedNodeTypeByKey(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, typeKey string, kinds map[string]*node.NodeType) (*node.NodeType, error) {
	if kind, cached := kinds[typeKey]; cached {
		return kind, nil
	}
	typeIDs, err := indexedIDs(ctx, tr, nodeTypeByKeyPrefix(orgID, typeKey))
	if err != nil {
		return nil, err
	}
	if len(typeIDs) > 1 {
		return nil, nodeOperationFailure(ctx, "read node type "+typeKey, fmt.Errorf("node type key %q is used by several node types", typeKey))
	}
	kinds[typeKey] = nil
	if len(typeIDs) == 0 {
		return nil, nil
	}
	encoded, err := tr.Get(fdb.Key(nodeTypeDefKey(orgID, typeIDs[0]))).Get()
	if err != nil {
		return nil, searchReadFailure(ctx, "read node type "+typeIDs[0].String(), err)
	}
	if len(encoded) == 0 {
		return nil, nil
	}
	var decoded node.NodeType
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, searchReadFailure(ctx, "decode node type "+typeKey, err)
	}
	if decoded.TypeKey != typeKey {
		return nil, nil
	}
	kinds[typeKey] = &decoded
	return &decoded, nil
}
