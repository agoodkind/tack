package foundationdb

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

var fenceIncrement = []byte{1, 0, 0, 0, 0, 0, 0, 0}

// fenceNodeMutation reads the activation record in tr with an ordinary read
// and atomically increments the node's write fence bucket epoch. If
// activation commits after tr's read version and before tr commits, tr
// conflicts. The retried mutation reads the active record. The function
// reports whether strict validation is active.
func fenceNodeMutation(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) (bool, error) {
	activation, err := readStrictActivation(ctx, tr)
	if err != nil {
		return false, err
	}
	tr.Add(fdb.Key(writeFenceBucketKey(node.WriteFenceBucket(orgID, nodeID))), fenceIncrement)
	return activation.Active, nil
}

// readStrictActivation reads the activation record in tr. It treats an absent
// record as inactive.
func readStrictActivation(ctx context.Context, tr fdb.ReadTransaction) (node.StrictActivation, error) {
	inactive := node.StrictActivation{Active: false, ActivatedAt: time.Time{}, ScanID: uuid.Nil, RecordsValidated: 0}
	encoded, err := tr.Get(fdb.Key(strictActivationKey())).Get()
	if err != nil {
		return inactive, searchReadFailure(ctx, "read the strict validation activation record", err)
	}
	if len(encoded) == 0 {
		return inactive, nil
	}
	var activation node.StrictActivation
	if err := json.Unmarshal(encoded, &activation); err != nil {
		return inactive, searchReadFailure(ctx, "decode the strict validation activation record", err)
	}
	return activation, nil
}

// decodeFenceEpoch decodes a little-endian bucket epoch. It returns zero for
// an absent epoch. It returns an error if the value has the wrong length or
// exceeds the signed int64 range.
func decodeFenceEpoch(ctx context.Context, bucket int, encoded []byte) (int64, error) {
	if len(encoded) == 0 {
		return 0, nil
	}
	if len(encoded) != len(fenceIncrement) {
		err := fmt.Errorf("write fence bucket %d epoch has %d bytes", bucket, len(encoded))
		return 0, searchReadFailure(ctx, "decode the write fence epoch", err)
	}
	value := binary.LittleEndian.Uint64(encoded)
	if value > math.MaxInt64 {
		err := fmt.Errorf("write fence bucket %d epoch %d exceeds the signed range", bucket, value)
		return 0, searchReadFailure(ctx, "decode the write fence epoch", err)
	}
	return int64(value), nil
}
