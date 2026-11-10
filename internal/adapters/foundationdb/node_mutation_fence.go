package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
)

// mutationFence is the write fence hook for the strict validation layer.
// Each product write transaction attempt calls the fence once for each
// written node before any write. The fence reports whether strict
// validation is active.
type mutationFence func(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) (bool, error)

// fenceAttempt calls the write fence of s for nodeID inside tr. It returns
// false when s does not have a write fence.
func (s *NodeStore) fenceAttempt(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) (bool, error) {
	return callFence(ctx, s.writeFence, tr, orgID, nodeID)
}

// fenceAttempt calls the write fence on s for nodeID inside tr. It returns
// false when s does not have a write fence.
func (s *RelationshipStore) fenceAttempt(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) (bool, error) {
	return callFence(ctx, s.writeFence, tr, orgID, nodeID)
}

// callFence calls fence for nodeID inside tr. It returns false when fence
// is nil.
func callFence(ctx context.Context, fence mutationFence, tr fdb.Transaction, orgID, nodeID uuid.UUID) (bool, error) {
	if fence == nil {
		return false, nil
	}
	return fence(ctx, tr, orgID, nodeID)
}
