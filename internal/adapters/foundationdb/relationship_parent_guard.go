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

// refuseOnlyParentRemoval refuses the removal of the edge from sourceID to
// targetID when that edge is the source node's only hierarchy parent. Search
// access compilation fails for a node without a hierarchy parent when the
// node type lists CanLiveUnder types. The guard applies the same rule: the
// source type lists CanLiveUnder types, node.LivesUnder places the source
// under the target, and no other edge from the source has a hierarchy parent
// as its target. The guard reads the source's edges in the removal
// transaction. FoundationDB aborts one of two concurrent transactions that
// each remove a different parent edge of the same node.
func refuseOnlyParentRemoval(ctx context.Context, tr fdb.Transaction, orgID, sourceID uuid.UUID, relationType string, targetID uuid.UUID) error {
	removedKey := fdb.Key(relationshipKey(orgID, sourceID, relationType, targetID))
	existing, err := tr.Get(removedKey).Get()
	if err != nil {
		return searchReadFailure(ctx, "read relationship "+relationType+" of node "+sourceID.String(), err)
	}
	if len(existing) == 0 {
		return nil
	}
	kinds := make(map[string]*node.NodeType)
	endpoints, err := hierarchyTypesOf(ctx, tr, orgID, []uuid.UUID{sourceID, targetID}, kinds)
	if err != nil {
		return err
	}
	source, target := endpoints[0], endpoints[1]
	if source == nil || len(source.CanLiveUnder) == 0 || !node.LivesUnder(source, target) {
		return nil
	}
	otherParent, err := hasOtherHierarchyParent(ctx, tr, orgID, sourceID, removedKey, targetID, source, kinds)
	if err != nil || otherParent {
		return err
	}
	wrapped := fmt.Errorf("node %s needs a hierarchy parent, and %s is its only hierarchy parent; move the node to another parent instead: %w",
		sourceID, targetID, domain.ErrFailedPrecondition)
	telemetry.L(ctx).ErrorContext(ctx, "relationship.remove_refused", slog.String("err", wrapped.Error()),
		slog.String("source_id", sourceID.String()), slog.String("target_id", targetID.String()))
	return loggedSearchError{err: wrapped}
}

// hasOtherHierarchyParent reads the edges from sourceID in bounded pages and
// reports whether an edge other than removedKey has a hierarchy parent of
// source as its target. Another edge to parentID keeps that parent.
func hasOtherHierarchyParent(
	ctx context.Context, tr fdb.Transaction, orgID, sourceID uuid.UUID, removedKey fdb.Key, parentID uuid.UUID,
	source *node.NodeType, kinds map[string]*node.NodeType,
) (bool, error) {
	keyRange, err := fdb.PrefixRange(relationshipPrefixBySource(orgID, sourceID, ""))
	if err != nil {
		return false, nodeOperationFailure(ctx, "create relationship range for node "+sourceID.String(), err)
	}
	end := fdb.FirstGreaterOrEqual(keyRange.End)
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	for {
		selection := fdb.SelectorRange{Begin: begin, End: end}
		items, readErr := tr.GetRange(selection, fdb.RangeOptions{Limit: maxRelationshipPage}).GetSliceWithError()
		if readErr != nil {
			return false, searchReadFailure(ctx, "read relationship page of node "+sourceID.String(), readErr)
		}
		candidates := make([]uuid.UUID, 0, len(items))
		for _, item := range items {
			if bytes.Equal(item.Key, removedKey) {
				continue
			}
			targetID, decodeErr := edgeEnd(ctx, item.Key)
			if decodeErr != nil {
				return false, decodeErr
			}
			if targetID == parentID {
				return true, nil
			}
			candidates = append(candidates, targetID)
		}
		targets, typeErr := hierarchyTypesOf(ctx, tr, orgID, candidates, kinds)
		if typeErr != nil {
			return false, typeErr
		}
		for _, target := range targets {
			if node.LivesUnder(source, target) {
				return true, nil
			}
		}
		if len(items) < maxRelationshipPage {
			return false, nil
		}
		begin = fdb.FirstGreaterThan(items[len(items)-1].Key)
	}
}

// hierarchyTypesOf returns the node type of each node in nodeIDs, in order.
// It issues every resolution read before it waits on any of them. A missing
// node, a node outside orgID, or a missing type yields nil. kinds caches one
// type read per type key.
func hierarchyTypesOf(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, nodeIDs []uuid.UUID, kinds map[string]*node.NodeType) ([]*node.NodeType, error) {
	resolves := make([]fdb.FutureByteSlice, len(nodeIDs))
	for position, nodeID := range nodeIDs {
		resolves[position] = tr.Get(fdb.Key(nodeResolveKey(nodeID)))
	}
	types := make([]*node.NodeType, len(nodeIDs))
	for position, future := range resolves {
		nodeID := nodeIDs[position]
		encoded, err := future.Get()
		if err != nil {
			return nil, searchReadFailure(ctx, "read node resolution "+nodeID.String(), err)
		}
		if len(encoded) == 0 {
			continue
		}
		var resolved node.NodeResolve
		if err := json.Unmarshal(encoded, &resolved); err != nil {
			return nil, searchReadFailure(ctx, "decode node resolution "+nodeID.String(), err)
		}
		if resolved.OrgID != orgID {
			continue
		}
		kind, err := cachedNodeTypeByKey(ctx, tr, orgID, resolved.NodeType, kinds)
		if err != nil {
			return nil, err
		}
		types[position] = kind
	}
	return types, nil
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
