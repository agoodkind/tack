package foundationdb

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// MetadataEpoch returns the metadata epoch values for the organizations in
// orgIDs as one string. The string lists each organization ID with its
// decimal epoch in organization ID order. It issues every epoch read in one
// transaction before it waits for any read. A stored property definition or
// node type change from the metadata stores changes the result. The one-time
// projection backfill leaves the epoch unchanged.
func (s *ViewStore) MetadataEpoch(ctx context.Context, orgIDs []uuid.UUID) (epoch string, err error) {
	defer telemetry.FDBOp(ctx, "store.view.metadata_epoch")(&err)
	sorted := slices.Clone(orgIDs)
	slices.SortFunc(sorted, func(left, right uuid.UUID) int { return strings.Compare(left.String(), right.String()) })
	sorted = slices.Compact(sorted)
	epochs := make([]int64, len(sorted))
	err = runNodeReadTransaction(ctx, s.db, "read metadata epochs", func(tr fdb.Transaction) error {
		futures := make([]fdb.FutureByteSlice, len(sorted))
		for position, orgID := range sorted {
			futures[position] = tr.Get(fdb.Key(searchEpochKey(orgID)))
		}
		for position, future := range futures {
			value, readErr := decodeEpochValue(ctx, future)
			if readErr != nil {
				return readErr
			}
			epochs[position] = value
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	var described strings.Builder
	for position, orgID := range sorted {
		described.WriteString(orgID.String())
		described.WriteByte('=')
		described.WriteString(strconv.FormatInt(epochs[position], 10))
		described.WriteByte(';')
	}
	return described.String(), nil
}

// decodeEpochValue waits for one epoch counter read. An absent counter is
// epoch zero.
func decodeEpochValue(ctx context.Context, future fdb.FutureByteSlice) (int64, error) {
	encoded, err := future.Get()
	if err != nil {
		return 0, searchReadFailure(ctx, "read metadata epoch", err)
	}
	if len(encoded) == 0 {
		return 0, nil
	}
	return unpackCounter(ctx, encoded)
}
