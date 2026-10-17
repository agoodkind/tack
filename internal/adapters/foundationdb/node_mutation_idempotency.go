package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// readCreateIdempotency reads the idempotency record for plan inside tr. It
// returns the node receipt if the fingerprint matches and the node exists.
// It returns an error that wraps domain.ErrConflict if the fingerprint differs
// or the node does not exist. It returns nil if plan does not specify an
// idempotency record or the key does not have a record.
func readCreateIdempotency(ctx context.Context, tr fdb.Transaction, plan node.MutationPlan) (*node.MutationReceipt, error) {
	if plan.Idempotency == nil {
		return nil, nil
	}
	encoded, err := tr.Get(fdb.Key(idempotencyKey(plan.OrgID, plan.Idempotency.Key))).Get()
	if err != nil {
		return nil, searchReadFailure(ctx, "read idempotency key", err)
	}
	if len(encoded) == 0 {
		return nil, nil
	}
	existing, err := decodeIdempotencyRecord(plan.Idempotency.Key, encoded)
	if err != nil {
		return nil, nodeOperationFailure(ctx, "decode idempotency record", err)
	}
	if existing.Fingerprint != "" && plan.Idempotency.Fingerprint != "" && existing.Fingerprint != plan.Idempotency.Fingerprint {
		return nil, mutationRefusal(ctx, plan.NodeID, "the idempotency key was used with a different request", domain.ErrConflict)
	}
	source, err := readMutationSource(ctx, tr, plan.OrgID, existing.NodeID)
	if err != nil {
		return nil, err
	}
	if !source.Exists {
		return nil, mutationRefusal(ctx, plan.NodeID, "the idempotency record identifies node "+existing.NodeID.String()+", which does not exist", domain.ErrConflict)
	}
	return &node.MutationReceipt{
		OrgID: plan.OrgID, NodeID: existing.NodeID, SourceDigest: node.Digest{}, ResultDigest: node.PrimaryDigest(source.Encoding),
	}, nil
}

// writeCreateIdempotencyRecord stores the idempotency record for plan inside tr.
func writeCreateIdempotencyRecord(ctx context.Context, tr fdb.Transaction, plan node.MutationPlan) error {
	if plan.Idempotency == nil {
		return nil
	}
	encoded, err := json.Marshal(plan.Idempotency)
	if err != nil {
		return nodeOperationFailure(ctx, "encode idempotency record", err)
	}
	tr.Set(fdb.Key(idempotencyKey(plan.OrgID, plan.Idempotency.Key)), encoded)
	return nil
}
