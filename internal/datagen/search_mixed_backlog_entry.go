package datagen

import (
	"context"
	"encoding/json"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// searchWorkFamily is the FoundationDB key family for pending search work records.
// Each key uses (search_work, class, bucket, orgID, nodeID).
const searchWorkFamily = "search_work"

// searchAgeEntry stores an age key's bucket, enqueue time in Unix nanoseconds, orgID, and nodeID.
type searchAgeEntry struct {
	bucket   int64
	enqueued int64
	orgID    string
	nodeID   string
}

// searchWorkEnqueued decodes the enqueued_at time from a pending search work record.
type searchWorkEnqueued struct {
	EnqueuedAt time.Time `json:"enqueued_at"`
}

func unpackSearchAgeKey(ctx context.Context, key []byte) (searchAgeEntry, error) {
	var entry searchAgeEntry
	elements, err := tuple.Unpack(key)
	if err != nil {
		return entry, loggedError(ctx, "the command could not decode the pending search work key", err)
	}
	if len(elements) != searchAgeKeyElements {
		return entry, errSearchAgeKey
	}
	bucket, bucketValid := elements[searchAgeBucket].(int64)
	enqueued, enqueuedValid := elements[searchAgeEnqueued].(int64)
	orgID, orgValid := elements[searchAgeOrg].(string)
	nodeID, nodeValid := elements[searchAgeNode].(string)
	if !bucketValid || !enqueuedValid || !orgValid || !nodeValid {
		return entry, errSearchAgeKey
	}
	return searchAgeEntry{bucket: bucket, enqueued: enqueued, orgID: orgID, nodeID: nodeID}, nil
}

// searchAgeEntryLive reports whether an age key has a work record with the same class, bucket,
// organization, node, and enqueue time.
func searchAgeEntryLive(
	ctx context.Context, snapshot fdb.Snapshot, prefix []byte, class searchdomain.WorkClass, entry searchAgeEntry,
) (bool, error) {
	key := prefixedKey(prefix, tuple.Tuple{searchWorkFamily, string(class), entry.bucket, entry.orgID, entry.nodeID})
	value, err := snapshot.Get(fdb.Key(key)).Get()
	if err != nil {
		return false, loggedError(ctx, "the command could not read a pending search work record for class "+string(class), err)
	}
	if len(value) == 0 {
		return false, nil
	}
	var record searchWorkEnqueued
	if err := json.Unmarshal(value, &record); err != nil {
		return false, loggedError(ctx, "the command could not decode a pending search work record for class "+string(class), err)
	}
	return record.EnqueuedAt.UnixNano() == entry.enqueued, nil
}
