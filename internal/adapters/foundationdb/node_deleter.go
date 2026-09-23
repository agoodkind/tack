package foundationdb

import (
	"context"

	"github.com/google/uuid"
)

// NodeDeleteStore exposes node deletion to services that depend on a delete-only boundary.
type NodeDeleteStore struct {
	nodes *NodeStore
}

// NewNodeDeleteStore creates the delete-only node boundary over nodes.
func NewNodeDeleteStore(nodes *NodeStore) *NodeDeleteStore {
	return &NodeDeleteStore{nodes: nodes}
}

// DeleteNode clears the requested node through the shared node store.
func (s *NodeDeleteStore) DeleteNode(ctx context.Context, orgID, nodeID uuid.UUID) error {
	return s.nodes.Delete(ctx, orgID, nodeID)
}
