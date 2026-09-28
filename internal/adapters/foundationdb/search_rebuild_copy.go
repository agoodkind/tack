package foundationdb

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// maxRebuildScanNodes bounds the nodes one copy scheduling step reads.
const maxRebuildScanNodes = 100

// CopyNextNodes reads the next bounded page of primary node records across
// every organization. In the same transaction, it schedules one copy item for
// each node without pending live work. The final page marks the scan
// complete.
func (s *SearchRebuildStore) CopyNextNodes(ctx context.Context, work searchdomain.Work, read searchdomain.Rebuild) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_rebuild.copy_nodes")(&err)
	err = s.rebuildStep(ctx, work, read, func(tr fdb.Transaction, _ searchWorkRecord) error {
		if read.State != searchdomain.RebuildCopying || read.ScanComplete {
			return searchdomain.ErrWorkChanged
		}
		items, err := readNodePage(ctx, tr, read.ScanCursor)
		if err != nil {
			return err
		}
		now := s.work.clock.Now().UTC()
		for _, item := range items[:min(len(items), maxRebuildScanNodes)] {
			orgID, nodeID, decodeErr := nodeInstanceIdentity(ctx, item.Key)
			if decodeErr != nil {
				return decodeErr
			}
			if scheduleErr := scheduleCopy(ctx, tr, now, orgID, nodeID); scheduleErr != nil {
				return scheduleErr
			}
		}
		next := read
		if len(items) > maxRebuildScanNodes {
			next.ScanCursor = base64.RawURLEncoding.EncodeToString(items[maxRebuildScanNodes-1].Key)
		} else {
			next.ScanCursor, next.ScanComplete = "", true
		}
		return writeSearchRecord(ctx, tr, searchRebuildKey(), rebuildRecordFor(next))
	})
	if err != nil {
		return searchStorageError(ctx, "search.rebuild.copy_failed", "schedule replacement copies", uuid.Nil, err)
	}
	return nil
}

// readNodePage reads at most one more than a page of primary node records
// after cursor. The extra record reports whether another page exists.
func readNodePage(ctx context.Context, tr fdb.Transaction, cursor string) ([]fdb.KeyValue, error) {
	keyRange, err := fdb.PrefixRange(nodeInstancePrefix())
	if err != nil {
		return nil, searchReadFailure(ctx, "create node scan range", err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if cursor != "" {
		lastKey, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
		if decodeErr != nil {
			return nil, searchReadFailure(ctx, "decode node scan cursor", decodeErr)
		}
		begin = fdb.FirstGreaterThan(fdb.Key(lastKey))
	}
	selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
	items, err := tr.GetRange(selection, fdb.RangeOptions{Limit: maxRebuildScanNodes + 1}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read node scan page", err)
	}
	return items, nil
}

// nodeInstanceIdentity decodes (node_instance, orgID, nodeType, nodeID).
func nodeInstanceIdentity(ctx context.Context, key fdb.Key) (uuid.UUID, uuid.UUID, error) {
	values, err := tuple.Unpack(stripPrefix(key))
	if err != nil {
		return uuid.Nil, uuid.Nil, searchReadFailure(ctx, "unpack node record key", err)
	}
	if len(values) != 4 {
		return uuid.Nil, uuid.Nil, searchReadFailure(ctx, "decode node record key", fmt.Errorf("node record key has %d tuple values", len(values)))
	}
	orgText, orgOK := values[1].(string)
	nodeText, nodeOK := values[3].(string)
	orgID, orgErr := uuid.Parse(orgText)
	nodeID, nodeErr := uuid.Parse(nodeText)
	if !orgOK || !nodeOK || orgErr != nil || nodeErr != nil {
		return uuid.Nil, uuid.Nil, searchReadFailure(ctx, "decode node record key", errors.New("node record key identity is invalid"))
	}
	return orgID, nodeID, nil
}

// scheduleCopy records one copy item for a node at its current generation
// and revision. A node with pending live work receives no copy. Every live
// claim during copying writes the replacement index as its mirror.
// Verification schedules a copy for any page that live work wrote before
// mirroring began. A node that search never indexed receives its first
// revision here, with the write versions of its authority.
func scheduleCopy(ctx context.Context, tr fdb.Transaction, now time.Time, orgID, nodeID uuid.UUID) error {
	if _, livePending, err := readSearchWork(ctx, tr, searchdomain.WorkClassLive, orgID, nodeID); err != nil || livePending {
		return err
	}
	generation, err := readSearchCounter(ctx, tr, searchGenerationKey(orgID, nodeID))
	if err != nil {
		return err
	}
	revision, err := readSearchCounter(ctx, tr, searchRevisionKey(orgID, nodeID))
	if err != nil {
		return err
	}
	if revision == 0 {
		if generation == 0 {
			if generation, err = incrementSearchCounter(ctx, tr, searchGenerationKey(orgID, nodeID)); err != nil {
				return err
			}
		}
		revision = generation
		writeSearchCounter(tr, searchRevisionKey(orgID, nodeID), revision)
		if err := initializeSearchAccess(ctx, tr, orgID, nodeID); err != nil {
			return err
		}
	}
	existing, found, err := readSearchWork(ctx, tr, searchdomain.WorkClassCopy, orgID, nodeID)
	if err != nil {
		return err
	}
	var previous *searchWorkRecord
	if found {
		previous = &existing
	}
	tr.Clear(fdb.Key(searchCursorKey(string(searchdomain.WorkClassCopy), orgID, nodeID)))
	return writeSearchWork(ctx, tr, searchdomain.WorkClassCopy, searchWorkRecord{
		OrgID: orgID, NodeID: nodeID, Generation: generation, Revision: revision, Deleted: false, EnqueuedAt: now,
	}, previous)
}

// CopiesPending reports whether any copy item remains.
func (s *SearchRebuildStore) CopiesPending(ctx context.Context) (bool, error) {
	pending, err := s.work.pendingBuckets(ctx, searchdomain.WorkClassCopy)
	if err != nil {
		return false, err
	}
	return len(pending) > 0, nil
}
