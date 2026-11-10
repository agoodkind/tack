package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// GetForUpdate reads the primary record for nodeID in orgID in one read transaction.
// GetForUpdate returns the decoded node and the SHA-256 digest of the stored bytes.
// If the node does not exist in orgID, GetForUpdate returns a nil node, a zero
// digest, and a nil error.
func (s *NodeStore) GetForUpdate(ctx context.Context, orgID, nodeID uuid.UUID) (current *node.Node, digest node.Digest, err error) {
	defer telemetry.FDBOp(ctx, "store.node.get_for_update")(&err)
	var source mutationSource
	err = runNodeReadTransaction(ctx, s.db, "read node "+nodeID.String()+" for an update", func(tr fdb.Transaction) error {
		var readErr error
		source, readErr = readMutationSource(ctx, tr, orgID, nodeID)
		return readErr
	})
	if err != nil || !source.Exists {
		return nil, node.Digest{}, err
	}
	return &source.Node, node.PrimaryDigest(source.Encoding), nil
}

// UpdateAtomicFrom writes like UpdateAtomic without changing relationships.
// UpdateAtomicFrom compares the stored record digest with readDigest from
// GetForUpdate inside the write transaction. If the digests differ,
// UpdateAtomicFrom logs node.mutation_refused, returns an error that wraps
// domain.ErrConflict, and does not change the node record, view, indexes, or
// relationships.
func (s *NodeStore) UpdateAtomicFrom(
	ctx context.Context, readDigest node.Digest, current *node.Node, view *node.NodeView, oldProps map[string]json.RawMessage,
	indexedProps []string, referenceKeys []node.ReferenceKey,
) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.update_atomic_from")(&err)
	return s.updateAtomic(ctx, &readDigest, current, view, oldProps, indexedProps, referenceKeys, nil)
}
