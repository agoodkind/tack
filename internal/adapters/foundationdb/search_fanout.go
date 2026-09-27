package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// scheduleDeletedFanout records each counterpart of a deleted node with one
// blind write and puts one access work item of the deleted node in the
// dependents phase. The work item schedules access work for the recorded
// counterparts in bounded slices. A delete without counterparts records no
// access work.
func scheduleDeletedFanout(ctx context.Context, tr fdb.Transaction, deletion searchWorkRecord, counterparts []uuid.UUID) error {
	recorded := 0
	for _, counterpartID := range counterparts {
		if counterpartID == deletion.NodeID || counterpartID == uuid.Nil {
			continue
		}
		tr.Set(fdb.Key(searchFanoutKey(deletion.OrgID, deletion.NodeID, counterpartID)), []byte{})
		recorded++
	}
	if recorded == 0 {
		return nil
	}
	class := searchdomain.WorkClassAccess
	if err := writeSearchWork(ctx, tr, class, deletion, nil); err != nil {
		return err
	}
	return writeSearchRecord(ctx, tr, searchCursorKey(string(class), deletion.OrgID, deletion.NodeID), searchProgressRecord{
		Revision: deletion.Revision, Generation: deletion.Generation, Projection: "",
		Cursor: "", Ordinal: 0, Phase: string(searchdomain.PhaseDependents),
	})
}

// readDeletedFanout reads at most limit recorded counterparts of a deleted
// node, starting at the first recorded counterpart. ScheduleDependents
// clears each counterpart it schedules.
func (s *SearchAccessStateStore) readDeletedFanout(ctx context.Context, work searchdomain.Work, limit int) (page node.IDPage, err error) {
	defer telemetry.FDBOp(ctx, "store.search_access.deleted_fanout")(&err)
	keyRange, err := fdb.PrefixRange(searchFanoutPrefix(work.OrgID, work.NodeID))
	if err != nil {
		return page, searchStorageError(ctx, "search.fanout.range_failed", "create deleted fanout range", work.NodeID, err)
	}
	var items []fdb.KeyValue
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var readErr error
		items, readErr = tr.GetRange(keyRange, fdb.RangeOptions{Limit: limit + 1}).GetSliceWithError()
		if readErr != nil {
			return searchReadFailure(ctx, "read deleted fanout", readErr)
		}
		return nil
	})
	if err != nil {
		return page, searchStorageError(ctx, "search.fanout.read_failed", "read deleted fanout", work.NodeID, err)
	}
	page = node.IDPage{IDs: make([]uuid.UUID, 0, min(len(items), limit)), NextCursor: "", Done: len(items) <= limit}
	for index, item := range items {
		if index == limit {
			break
		}
		counterpartID, decodeErr := lastTupleID(ctx, item.Key)
		if decodeErr != nil {
			return node.IDPage{}, searchStorageError(ctx, "search.fanout.decode_failed", "decode deleted fanout", work.NodeID, decodeErr)
		}
		page.IDs = append(page.IDs, counterpartID)
	}
	return page, nil
}

// finishDeletedClass clears the finished work class of a deleted node. The
// cleanup and access classes of a deleted node finish independently. The
// class that finishes last clears every remaining search key of the node.
func finishDeletedClass(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, record searchWorkRecord) error {
	clearSearchWork(tr, work, record)
	other := searchdomain.WorkClassAccess
	if work.Class == searchdomain.WorkClassAccess {
		other = searchdomain.WorkClassCleanup
	}
	_, pending, err := readSearchWork(ctx, tr, other, work.OrgID, work.NodeID)
	if err != nil || pending {
		return err
	}
	return clearDeletedSearchState(ctx, tr, work)
}

// clearDeletedSearchState removes every search key of a deleted node after
// its documents are retired and its counterparts are scheduled.
func clearDeletedSearchState(ctx context.Context, tr fdb.Transaction, work searchdomain.Work) error {
	tr.Clear(fdb.Key(searchGenerationKey(work.OrgID, work.NodeID)))
	tr.Clear(fdb.Key(searchRevisionKey(work.OrgID, work.NodeID)))
	tr.Clear(fdb.Key(searchAccessKey(work.OrgID, work.NodeID)))
	fanout, err := fdb.PrefixRange(searchFanoutPrefix(work.OrgID, work.NodeID))
	if err != nil {
		return searchReadFailure(ctx, "create deleted fanout range", err)
	}
	tr.ClearRange(fanout)
	bucket := searchBucket(work.OrgID, work.NodeID)
	for _, class := range searchNodeClasses {
		existing, found, err := readSearchWork(ctx, tr, class, work.OrgID, work.NodeID)
		if err != nil {
			return err
		}
		if found {
			removeSearchWork(tr, class, existing)
		}
		tr.Clear(fdb.Key(searchClaimKey(string(class), bucket, work.OrgID, work.NodeID)))
		tr.Clear(fdb.Key(searchCursorKey(string(class), work.OrgID, work.NodeID)))
		tr.Clear(fdb.Key(searchErrorKey(string(class), work.OrgID, work.NodeID)))
	}
	return nil
}
