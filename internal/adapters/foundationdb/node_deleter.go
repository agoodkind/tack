package foundationdb

import (
	"context"

	"github.com/google/uuid"
)

// NodeDeleteStore exposes only node deletion from a [NodeStore].
type NodeDeleteStore struct {
	nodes *NodeStore
}

// NewNodeDeleteStore creates a NodeDeleteStore that uses nodes for deletion.
//
// The returned store retains the supplied NodeStore. Its direct deletion
// method uses this call:
//
//   - DeleteNode calls nodes.Delete.
func NewNodeDeleteStore(nodes *NodeStore) *NodeDeleteStore {
	return &NodeDeleteStore{nodes: nodes}
}

// DeleteNode deletes a node with NodeStore.Delete.
//
// It passes the supplied context and identifiers to the underlying store.
// The arguments identify the deletion:
//
//   - orgID selects the organization.
//   - nodeID selects the node.
func (s *NodeDeleteStore) DeleteNode(ctx context.Context, orgID, nodeID uuid.UUID) error {
	return s.nodes.Delete(ctx, orgID, nodeID)
}
