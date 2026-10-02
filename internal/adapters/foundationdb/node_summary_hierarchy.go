package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/searchaccess"
)

// summaryHierarchy reads hierarchy state inside the one read transaction of
// a summary batch. It issues the reads of one level before it waits on any
// of them and reads each node type once per organization and type key.
type summaryHierarchy struct {
	tr    fdb.Transaction
	kinds map[orgTypeKey]*node.NodeType
}

type orgTypeKey struct {
	orgID   uuid.UUID
	typeKey string
}

var _ searchaccess.HierarchyReader = (*summaryHierarchy)(nil)

func newSummaryHierarchy(tr fdb.Transaction) *summaryHierarchy {
	return &summaryHierarchy{tr: tr, kinds: map[orgTypeKey]*node.NodeType{}}
}

// Ancestors reads each node's resolve record and view, its node type, and,
// below an entry point, every child_of target.
func (h *summaryHierarchy) Ancestors(ctx context.Context, nodes []searchaccess.OrgNode) (map[searchaccess.OrgNode]searchaccess.AncestorState, error) {
	ids := make([]uuid.UUID, len(nodes))
	for position, key := range nodes {
		ids[position] = key.NodeID
	}
	views := make([]*node.NodeView, len(nodes))
	if err := readSummaryViews(h.tr, ids, views); err != nil {
		return nil, err
	}
	states := make(map[searchaccess.OrgNode]searchaccess.AncestorState, len(nodes))
	edges := make([]fdb.RangeResult, len(nodes))
	for position, key := range nodes {
		view := views[position]
		if view == nil {
			states[key] = searchaccess.AncestorState{Exists: false, OrgID: uuid.Nil, TypeKey: "", Type: nil, ChildOfs: nil}
			continue
		}
		if view.OrgID != key.OrgID {
			states[key] = searchaccess.AncestorState{Exists: true, OrgID: view.OrgID, TypeKey: "", Type: nil, ChildOfs: nil}
			continue
		}
		kind, err := h.nodeType(ctx, key.OrgID, view.NodeType)
		if err != nil {
			return nil, err
		}
		states[key] = searchaccess.AncestorState{Exists: true, OrgID: view.OrgID, TypeKey: view.NodeType, Type: kind, ChildOfs: nil}
		if kind == nil || kind.Features.Has(node.FeatureIsEntryPoint) {
			continue
		}
		keyRange, err := fdb.PrefixRange(relationshipPrefixBySource(key.OrgID, key.NodeID, node.RelChildOf))
		if err != nil {
			return nil, searchReadFailure(ctx, "create child_of range of node "+key.NodeID.String(), err)
		}
		edges[position] = h.tr.GetRange(keyRange, fdb.RangeOptions{})
	}
	for position, key := range nodes {
		state, found := states[key]
		if !found || state.Type == nil || state.Type.Features.Has(node.FeatureIsEntryPoint) {
			continue
		}
		items, err := edges[position].GetSliceWithError()
		if err != nil {
			return nil, searchReadFailure(ctx, "read child_of edges of node "+key.NodeID.String(), err)
		}
		state.ChildOfs = make([]uuid.UUID, 0, len(items))
		for _, item := range items {
			targetID, err := edgeEnd(ctx, item.Key)
			if err != nil {
				return nil, err
			}
			state.ChildOfs = append(state.ChildOfs, targetID)
		}
		states[key] = state
	}
	return states, nil
}

// Targets reads each node's resolve record and, inside the requested
// organization, its node type.
func (h *summaryHierarchy) Targets(ctx context.Context, nodes []searchaccess.OrgNode) (map[searchaccess.OrgNode]searchaccess.TargetState, error) {
	resolves := make([]fdb.FutureByteSlice, len(nodes))
	for position, key := range nodes {
		resolves[position] = h.tr.Get(fdb.Key(nodeResolveKey(key.NodeID)))
	}
	states := make(map[searchaccess.OrgNode]searchaccess.TargetState, len(nodes))
	for position, key := range nodes {
		encoded, err := resolves[position].Get()
		if err != nil {
			return nil, searchReadFailure(ctx, "read resolve record of node "+key.NodeID.String(), err)
		}
		if len(encoded) == 0 {
			states[key] = searchaccess.TargetState{Exists: false, OrgID: uuid.Nil, Type: nil}
			continue
		}
		var resolved node.NodeResolve
		if err := json.Unmarshal(encoded, &resolved); err != nil {
			return nil, searchReadFailure(ctx, "decode resolve record of node "+key.NodeID.String(), err)
		}
		if resolved.OrgID != key.OrgID {
			states[key] = searchaccess.TargetState{Exists: true, OrgID: resolved.OrgID, Type: nil}
			continue
		}
		kind, err := h.nodeType(ctx, key.OrgID, resolved.NodeType)
		if err != nil {
			return nil, err
		}
		states[key] = searchaccess.TargetState{Exists: true, OrgID: resolved.OrgID, Type: kind}
	}
	return states, nil
}

// nodeType returns the node type of orgID that uses typeKey, or nil when no
// node type uses it. It returns an error when several node types use it.
func (h *summaryHierarchy) nodeType(ctx context.Context, orgID uuid.UUID, typeKey string) (*node.NodeType, error) {
	cacheKey := orgTypeKey{orgID: orgID, typeKey: typeKey}
	if kind, read := h.kinds[cacheKey]; read {
		return kind, nil
	}
	typeIDs, err := indexedIDs(ctx, h.tr, nodeTypeByKeyPrefix(orgID, typeKey))
	if err != nil {
		return nil, err
	}
	if len(typeIDs) > 1 {
		return nil, searchReadFailure(ctx, "read node type "+typeKey, fmt.Errorf("node type key %q is used by several node types", typeKey))
	}
	var kind *node.NodeType
	if len(typeIDs) == 1 {
		kind, err = h.decodeNodeType(ctx, orgID, typeIDs[0], typeKey)
		if err != nil {
			return nil, err
		}
	}
	h.kinds[cacheKey] = kind
	return kind, nil
}

func (h *summaryHierarchy) decodeNodeType(ctx context.Context, orgID, typeID uuid.UUID, typeKey string) (*node.NodeType, error) {
	encoded, err := h.tr.Get(fdb.Key(nodeTypeDefKey(orgID, typeID))).Get()
	if err != nil {
		return nil, searchReadFailure(ctx, "read node type "+typeID.String(), err)
	}
	if len(encoded) == 0 {
		return nil, nil
	}
	var kind node.NodeType
	if err := json.Unmarshal(encoded, &kind); err != nil {
		return nil, searchReadFailure(ctx, "decode node type "+typeKey, err)
	}
	if kind.TypeKey != typeKey {
		return nil, nil
	}
	return &kind, nil
}
