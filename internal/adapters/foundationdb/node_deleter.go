package foundationdb

import (
	"context"

	"github.com/google/uuid"
)

// NodeDeleteStore exposes only node deletion from a [NodeStore].
type NodeDeleteStore struct {
	nodes *NodeStore
}

// NewNodeDeleteStore creates a NodeDeleteStore that deletes through nodes.
func NewNodeDeleteStore(nodes *NodeStore) *NodeDeleteStore {
	return &NodeDeleteStore{nodes: nodes}
}

// DeleteNode deletes the node with [NodeStore.Delete].
func (s *NodeDeleteStore) DeleteNode(ctx context.Context, orgID, nodeID uuid.UUID) error {
	return s.nodes.Delete(ctx, orgID, nodeID)
}
