package foundationdb

import (
	"context"
	"encoding/hex"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
)

// advanceMutationCheckpoint advances the repair checkpoint of write inside tr
// when write is not nil. A completed checkpoint of an applied mutation
// records the primary digest of encoded as its result digest. A deletion
// passes a nil encoded and leaves the result digest empty.
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
