package foundationdb

import (
	"context"
	"encoding/hex"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
)

// advanceMutationCheckpoint advances the repair checkpoint of write inside
// tr when write is not nil. For a completed mutation with nonnil encoded
// bytes, it sets the result digest to the primary digest of encoded. A
// deletion passes nil for encoded, and this function leaves the result
// digest from write.Next unchanged. The repair deletion caller starts with
// an empty result digest.
func advanceMutationCheckpoint(ctx context.Context, tr fdb.Transaction, write *node.CheckpointWrite, encoded []byte) error {
	if write == nil {
		return nil
	}
	next := write.Next
	if next.Phase == node.RepairPhaseCompleted && encoded != nil {
		digest := node.PrimaryDigest(encoded)
		next.ResultDigest = hex.EncodeToString(digest[:])
	}
	return advanceRepairCheckpoint(ctx, tr, write.Previous, next)
}
