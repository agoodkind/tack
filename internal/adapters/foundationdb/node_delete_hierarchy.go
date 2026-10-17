package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// hierarchyNode is one node of a subtree delete, with its type.
type hierarchyNode struct {
	ID      uuid.UUID
	TypeKey string
	Kind    *node.NodeType
	Found   bool
}

// pendingResolve is one resolution read issued before any read is awaited.
type pendingResolve struct {
	nodeID uuid.UUID
	future fdb.FutureByteSlice
}

// readHierarchyNodes resolves each node in nodeIDs inside tr and returns the
// nodes in order. It issues every resolution read before it waits on any of
// them. A missing node or a node outside orgID has Found false. A node of a
// missing type has Found true and a nil Kind. kinds caches one type read per
// type key.
func readHierarchyNodes(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, nodeIDs []uuid.UUID, kinds map[string]*node.NodeType) ([]hierarchyNode, error) {
	pending := make([]pendingResolve, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		pending = append(pending, pendingResolve{nodeID: nodeID, future: tr.Get(fdb.Key(nodeResolveKey(nodeID)))})
	}
	nodes := make([]hierarchyNode, 0, len(pending))
	for _, read := range pending {
		encoded, err := read.future.Get()
		if err != nil {
			return nil, searchReadFailure(ctx, "read node resolution "+read.nodeID.String(), err)
		}
		missing := hierarchyNode{ID: read.nodeID, TypeKey: "", Kind: nil, Found: false}
		if len(encoded) == 0 {
			nodes = append(nodes, missing)
			continue
		}
		var resolved node.NodeResolve
		if err := json.Unmarshal(encoded, &resolved); err != nil {
			return nil, searchReadFailure(ctx, "decode node resolution "+read.nodeID.String(), err)
		}
		if resolved.OrgID != orgID {
			nodes = append(nodes, missing)
			continue
		}
		kind, err := cachedNodeTypeByKey(ctx, tr, orgID, resolved.NodeType, kinds)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, hierarchyNode{ID: read.nodeID, TypeKey: resolved.NodeType, Kind: kind, Found: true})
	}
	return nodes, nil
}

// hasHierarchyParent reads the child_of edges of source inside tr and reports
// whether node.HierarchyParent identifies parentID as the hierarchy parent of
// source. hasHierarchyParent returns any node.HierarchyParent error, which
// matches node.ErrNoHierarchyParent. The subtree delete step fails without
// deleting or moving a node when hasHierarchyParent returns that error.
func hasHierarchyParent(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, source hierarchyNode, parentID uuid.UUID) (bool, error) {
	edges, err := readDeleteEdges(ctx, tr, orgID, source.ID, node.RelChildOf, false)
	if err != nil {
		return false, err
	}
	targets := make([]uuid.UUID, 0, len(edges))
	for _, edge := range edges {
		targets = append(targets, edge.TargetID)
	}
	found, parentErr := node.HierarchyParent(source.ID, source.Kind, targets)
	if parentErr != nil {
		return false, nodeOperationFailure(ctx, "read the hierarchy parent of node "+source.ID.String(), parentErr)
	}
	return found == parentID, nil
}
