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

// refuseOnlyParentRemoval runs inside the relationship removal transaction
// and allows removal of edges of other relation types and child_of edges that
// do not exist. When the source node stores parent_id, the guard refuses
// removal of the child_of edge targeting parent_id for every node type, even
// when another child_of edge remains, and allows removal of other child_of
// edges. When the source node does not store parent_id, the guard refuses
// removal if node.HierarchyParent returns an error for the source type and
// the other remaining child_of target, if any. Each refusal returns an error
// that matches domain.ErrFailedPrecondition. FoundationDB aborts one of two
// concurrent transactions that each remove a different parent edge of the
// same node.
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
	canonicalID, err := storedParentID(ctx, tr, orgID, sources[0])
	if err != nil {
		return err
	}
	if canonicalID != uuid.Nil && canonicalID != targetID {
		return nil
	}
	if canonicalID == uuid.Nil {
		remaining, err := otherParentTarget(ctx, tr, orgID, sourceID, removedKey)
		if err != nil {
			return err
		}
		if _, parentErr := node.HierarchyParent(sourceID, sources[0].Kind, remaining); parentErr == nil {
			return nil
		}
	}
	wrapped := fmt.Errorf("node %s needs a hierarchy parent, and %s is its only hierarchy parent; move the node to another parent instead: %w",
		sourceID, targetID, domain.ErrFailedPrecondition)
	telemetry.L(ctx).ErrorContext(ctx, "relationship.remove_refused", slog.String("err", wrapped.Error()),
		slog.String("source_id", sourceID.String()), slog.String("target_id", targetID.String()))
	return loggedSearchError{err: wrapped}
}

// storedParentID reads the primary record of source inside tr and returns the
// parent_id that the record stores.
// The function returns [uuid.Nil] when source does not identify a node in the
// organization, the primary record does not exist, or parent_id does not
// contain a UUID string.
func storedParentID(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, source hierarchyNode) (uuid.UUID, error) {
	if !source.Found {
		return uuid.Nil, nil
	}
	current, _, err := readNodeAndView(ctx, tr, orgID, source)
	if err != nil || current == nil {
		return uuid.Nil, err
	}
	return parentIDValue(current.Props[parentIDProperty]), nil
}

// parentIDValue decodes one raw parent_id property value and returns the UUID
// that the JSON string contains.
// The function returns [uuid.Nil] when the value does not exist, does not
// encode a JSON string, or does not contain a UUID.
func parentIDValue(raw json.RawMessage) uuid.UUID {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return uuid.Nil
	}
	parentID, parseErr := uuid.Parse(text)
	if parseErr != nil {
		return uuid.Nil
	}
	return parentID
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
