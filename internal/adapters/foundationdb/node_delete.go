package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
)

// NodeDeleteStore clears all FDB records for a node in one transaction.
// It delegates to NodeStore.Delete, which owns the concrete key-clearing logic
// (primary, view, resolve, property indexes, all relationships where the node
// is source or target).
type NodeDeleteStore struct {
	nodes *NodeStore
}

func NewNodeDeleteStore(db fdb.Database) *NodeDeleteStore {
	return &NodeDeleteStore{nodes: NewNodeStore(db)}
}

func (s *NodeDeleteStore) DeleteNode(ctx context.Context, orgID, nodeID uuid.UUID) error {
	return s.nodes.Delete(ctx, orgID, nodeID)
}
