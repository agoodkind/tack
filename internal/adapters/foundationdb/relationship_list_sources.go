package foundationdb

import (
	"context"
	"errors"
	"strconv"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// ListBySources reads relationships for all sources in one read transaction.
// It starts one range read per source before waiting for any results.
func (s *RelationshipStore) ListBySources(
	ctx context.Context,
	orgID uuid.UUID,
	sourceIDs []uuid.UUID,
	relationType string,
) (bySource map[uuid.UUID][]*node.Relationship, err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.list_by_sources")(&err)
	ranges := make([]fdb.KeyRange, 0, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		keyRange, prefixErr := fdb.PrefixRange(relationshipPrefixBySource(orgID, sourceID, relationType))
		if prefixErr != nil {
			return nil, sourcesReadError{sourceCount: len(sourceIDs), err: prefixErr}
		}
		ranges = append(ranges, keyRange)
	}
	transaction, createErr := s.db.CreateTransaction()
	if createErr != nil {
		return nil, sourcesReadError{sourceCount: len(sourceIDs), err: createErr}
	}
	defer transaction.Cancel()
	for {
		grouped, readErr := readSourceRanges(transaction, sourceIDs, ranges)
		if readErr == nil {
			return grouped, nil
		}
		var databaseError fdb.Error
		if !errors.As(readErr, &databaseError) {
			return nil, sourcesReadError{sourceCount: len(sourceIDs), err: readErr}
		}
		if retryErr := transaction.OnError(databaseError).Get(); retryErr != nil {
			return nil, sourcesReadError{sourceCount: len(sourceIDs), err: retryErr}
		}
	}
}

// readSourceRanges starts all range reads before waiting for results and
// returns decoded relationships keyed by source ID. It returns a
// sourceRangeError if a range read fails.
func readSourceRanges(
	tr fdb.ReadTransaction,
	sourceIDs []uuid.UUID,
	ranges []fdb.KeyRange,
) (map[uuid.UUID][]*node.Relationship, error) {
	// Each tr.GetRange call sends its first read request immediately.
	// This loop starts every range read. The next loop waits for the results.
	futures := make([]fdb.RangeResult, 0, len(ranges))
	for _, keyRange := range ranges {
		futures = append(futures, tr.GetRange(keyRange, fdb.RangeOptions{}))
	}
	grouped := make(map[uuid.UUID][]*node.Relationship, len(sourceIDs))
	for index, future := range futures {
		keyValues, readErr := future.GetSliceWithError()
		if readErr != nil {
			return nil, sourceRangeError{sourceID: sourceIDs[index], err: readErr}
		}
		grouped[sourceIDs[index]] = decodeRelationships(keyValues)
	}
	return grouped, nil
}

// sourceRangeError adds the source ID to an error from a failed range read.
// The Unwrap method returns the original error.
// The ListBySources retry loop uses [errors.As] to find a FoundationDB
// error inside a sourceRangeError.
type sourceRangeError struct {
	sourceID uuid.UUID
	err      error
}

func (e sourceRangeError) Error() string {
	return "read relationships of source " + e.sourceID.String() + ": " + e.err.Error()
}

func (e sourceRangeError) Unwrap() error { return e.err }

// sourcesReadError adds the source count to the error message.
// ListBySources returns a sourcesReadError for each failure that ends
// the call. Unwrap returns the original error.
// ListBySources does not log the failure. The caller logs the failure.
type sourcesReadError struct {
	sourceCount int
	err         error
}

func (e sourcesReadError) Error() string {
	return "fdb list rels by " + strconv.Itoa(e.sourceCount) + " sources: " + e.err.Error()
}

func (e sourcesReadError) Unwrap() error { return e.err }
