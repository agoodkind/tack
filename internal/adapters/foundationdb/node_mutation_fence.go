package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
)

// mutationFence is the write fence for the strict validation layer. Each
// ApplyMutation and DeleteNode transaction attempt calls the fence once for
// the mutated node after all expectation checks and before any write. The
// fence reports whether strict validation is active.
type mutationFence func(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) (bool, error)

// fenceAttempt calls the write fence of s for nodeID inside tr. It returns
// false when s does not have a write fence.
func (s *NodeStore) fenceAttempt(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) (bool, error) {
	if s.writeFence == nil {
		return false, nil
	}
	return s.writeFence(ctx, tr, orgID, nodeID)
}

// refuseStrictResult returns an error that wraps domain.ErrFailedPrecondition
// when strict is true. ApplyMutation does not validate complete results and
// refuses every mutation while strict validation is active.
func refuseStrictResult(ctx context.Context, nodeID uuid.UUID, strict bool) error {
	if !strict {
		return nil
	}
	return mutationRefusal(ctx, nodeID, "strict validation is active, and ApplyMutation does not validate complete results", domain.ErrFailedPrecondition)
}
