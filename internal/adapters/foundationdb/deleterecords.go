package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// readNodeAndView decodes the primary and view records of target and returns
// the primary encoding. When the primary does not exist, it returns nil for
// both records and the encoding. A missing view produces a nil view.
func readNodeAndView(
	ctx context.Context,
	transaction fdb.Transaction,
	orgID uuid.UUID,
	target hierarchyNode,
) (*node.Node, *node.NodeView, []byte, error) {
	nodeFuture := transaction.Get(fdb.Key(nodeInstanceKey(orgID, target.TypeKey, target.ID)))
	viewFuture := transaction.Get(fdb.Key(nodeViewKey(orgID, target.TypeKey, target.ID)))
	encodedNode, err := nodeFuture.Get()
	if err != nil {
		return nil, nil, nil, searchReadFailure(ctx, "read node "+target.ID.String(), err)
	}

	encodedView, err := viewFuture.Get()
	if err != nil {
		return nil, nil, nil, searchReadFailure(ctx, "read node view "+target.ID.String(), err)
	}

	if len(encodedNode) == 0 {
		return nil, nil, nil, nil
	}

	var current node.Node
	if err := json.Unmarshal(encodedNode, &current); err != nil {
		return nil, nil, nil, nodeOperationFailure(ctx, "decode node "+target.ID.String(), err)
	}

	if len(encodedView) == 0 {
		return &current, nil, encodedNode, nil
	}

	var view node.NodeView
	if err := json.Unmarshal(encodedView, &view); err != nil {
		return nil, nil, nil, nodeOperationFailure(ctx, "decode node view "+target.ID.String(), err)
	}

	return &current, &view, encodedNode, nil
}

// readRootParent derives the parent of rootID from its child_of edges.
// It returns [uuid.Nil] for missing edges, more edges than one page permits,
// or a parent outside orgID. Conflicting targets return an error that matches
// node.ErrNoHierarchyParent and prevent subtree deletion.
func readRootParent(
	ctx context.Context,
	transaction fdb.Transaction,
	orgID uuid.UUID,
	rootID uuid.UUID,
) (uuid.UUID, error) {
	edges, err := readDeleteEdges(ctx, transaction, orgID, rootID, node.RelChildOf, false)
	if err != nil || len(edges) == 0 || len(edges) > maxDeleteEdgePage {
		return uuid.Nil, err
	}

	targets := make([]uuid.UUID, 0, len(edges))
	for _, edge := range edges {
		targets = append(targets, edge.TargetID)
	}

	parentID, parentErr := node.HierarchyParent(rootID, nil, targets)
	if parentErr != nil {
		return uuid.Nil, nodeOperationFailure(
			ctx,
			"read the hierarchy parent of deleted node "+rootID.String(),
			parentErr,
		)
	}

	parents, err := readHierarchyNodes(
		ctx,
		transaction,
		orgID,
		[]uuid.UUID{parentID},
		make(map[string]*node.NodeType),
	)
	if err != nil || !parents[0].Found {
		return uuid.Nil, err
	}

	return parentID, nil
}
