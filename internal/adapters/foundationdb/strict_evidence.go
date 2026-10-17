package foundationdb

import (
	"context"
	"errors"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// CaptureStrictEvidence reads every write fence bucket epoch and every stored
// organization metadata epoch in one transaction.
func (s *NodeStore) CaptureStrictEvidence(ctx context.Context) (evidence node.StrictEvidence, err error) {
	defer telemetry.FDBOp(ctx, "store.strict.capture_evidence")(&err)
	err = runNodeReadTransaction(ctx, s.db, "capture strict validation evidence", func(tr fdb.Transaction) error {
		var readErr error
		evidence, readErr = readStrictEvidence(ctx, tr)
		return readErr
	})
	return evidence, err
}

// readStrictEvidence reads the bucket epochs and metadata epochs in tr with
// ordinary reads. If another transaction commits a write to one of those keys
// after tr's read version, tr conflicts when it attempts to commit a write.
func readStrictEvidence(ctx context.Context, tr fdb.ReadTransaction) (node.StrictEvidence, error) {
	evidence := node.StrictEvidence{Buckets: make([]int64, node.WriteFenceBuckets), MetadataEpochs: map[uuid.UUID]int64{}}
	futures := make([]fdb.FutureByteSlice, node.WriteFenceBuckets)
	for bucket := range futures {
		futures[bucket] = tr.Get(fdb.Key(writeFenceBucketKey(bucket)))
	}
	epochRange, err := fdb.PrefixRange(withPrefix(tuple.Tuple{keySearchEpoch}.Pack()))
	if err != nil {
		return evidence, searchReadFailure(ctx, "create the metadata epoch range", err)
	}
	epochs, err := tr.GetRange(epochRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return evidence, searchReadFailure(ctx, "read the metadata epochs", err)
	}
	for bucket, future := range futures {
		encoded, readErr := future.Get()
		if readErr != nil {
			return evidence, searchReadFailure(ctx, "read the write fence epochs", readErr)
		}
		evidence.Buckets[bucket], err = decodeFenceEpoch(ctx, bucket, encoded)
		if err != nil {
			return evidence, err
		}
	}
	for _, item := range epochs {
		orgID, decodeErr := metadataEpochOrganization(ctx, item.Key)
		if decodeErr != nil {
			return evidence, decodeErr
		}
		evidence.MetadataEpochs[orgID], err = unpackCounter(ctx, item.Value)
		if err != nil {
			return evidence, err
		}
	}
	return evidence, nil
}

func metadataEpochOrganization(ctx context.Context, key fdb.Key) (uuid.UUID, error) {
	values, err := tuple.Unpack(stripPrefix(key))
	if err != nil {
		return uuid.Nil, searchReadFailure(ctx, "decode a metadata epoch key", err)
	}
	if len(values) != 2 {
		return uuid.Nil, searchReadFailure(ctx, "decode a metadata epoch key", fmt.Errorf("key has %d tuple values", len(values)))
	}
	text, ok := values[1].(string)
	if !ok {
		return uuid.Nil, searchReadFailure(ctx, "decode a metadata epoch key", errors.New("key has no organization identifier"))
	}
	orgID, err := uuid.Parse(text)
	if err != nil {
		return uuid.Nil, searchReadFailure(ctx, "parse a metadata epoch organization", err)
	}
	return orgID, nil
}
