package foundationdb

import (
	"context"
	"errors"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// writeSearchWork writes record for class together with its class-age
// entry. A rewrite of a previous record stores the earlier of the two
// enqueue times. It clears the previous class-age entry and writes the
// class-age entry at the stored enqueue time.
func writeSearchWork(ctx context.Context, tr fdb.Transaction, class searchdomain.WorkClass, record searchWorkRecord, previous *searchWorkRecord) error {
	bucket := searchBucket(record.OrgID, record.NodeID)
	if previous != nil {
		record.EnqueuedAt = earliestSearchTime(previous.EnqueuedAt, record.EnqueuedAt)
		tr.Clear(fdb.Key(searchAgeKey(string(class), bucket, previous.EnqueuedAt, previous.OrgID, previous.NodeID)))
	}
	tr.Set(fdb.Key(searchAgeKey(string(class), bucket, record.EnqueuedAt, record.OrgID, record.NodeID)), []byte{})
	return writeSearchRecord(ctx, tr, searchWorkKey(string(class), bucket, record.OrgID, record.NodeID), record)
}

// removeSearchWork clears record and its class-age entry.
func removeSearchWork(tr fdb.Transaction, class searchdomain.WorkClass, record searchWorkRecord) {
	bucket := searchBucket(record.OrgID, record.NodeID)
	tr.Clear(fdb.Key(searchWorkKey(string(class), bucket, record.OrgID, record.NodeID)))
	tr.Clear(fdb.Key(searchAgeKey(string(class), bucket, record.EnqueuedAt, record.OrgID, record.NodeID)))
}

// readAgedWork reads the pending record of the organization and node in one
// class-age entry. It reports false for an entry without a record at the
// same enqueue time.
func readAgedWork(ctx context.Context, tr fdb.Transaction, class searchdomain.WorkClass, bucket int, ageKey fdb.Key) (searchWorkRecord, bool, error) {
	values, err := tuple.Unpack(stripPrefix(ageKey))
	if err != nil {
		return searchWorkRecord{}, false, searchReadFailure(ctx, "unpack search age key", err)
	}
	if len(values) != 6 {
		return searchWorkRecord{}, false, searchReadFailure(ctx, "decode search age key", fmt.Errorf("search age key has %d values", len(values)))
	}
	enqueued, enqueuedOK := values[3].(int64)
	orgText, orgOK := values[4].(string)
	nodeText, nodeOK := values[5].(string)
	orgID, orgErr := uuid.Parse(orgText)
	nodeID, nodeErr := uuid.Parse(nodeText)
	if !enqueuedOK || !orgOK || !nodeOK || orgErr != nil || nodeErr != nil {
		return searchWorkRecord{}, false, searchReadFailure(ctx, "decode search age key", errors.New("search age key identity is invalid"))
	}
	record, found, err := readSearchWork(ctx, tr, class, orgID, nodeID)
	if err != nil || !found {
		return record, false, err
	}
	return record, searchBucket(orgID, nodeID) == bucket && record.EnqueuedAt.UnixNano() == enqueued, nil
}

// readSearchWork reads the pending record of class for one node.
func readSearchWork(ctx context.Context, tr fdb.Transaction, class searchdomain.WorkClass, orgID, nodeID uuid.UUID) (searchWorkRecord, bool, error) {
	var existing searchWorkRecord
	key := searchWorkKey(string(class), searchBucket(orgID, nodeID), orgID, nodeID)
	found, err := readSearchRecord(ctx, tr, key, &existing)
	return existing, found, err
}
