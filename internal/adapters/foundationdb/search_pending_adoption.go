package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// adoptPendingWrite reads the exact per-node key written by deployed releases.
// The transaction preserves its document/index identity before clearing that key.
// A qualified record with a later original deadline already preserves the wait.
func adoptPendingWrite(ctx context.Context, tr fdb.Transaction, work searchdomain.Work) error {
	legacyKey := searchPendingWritePrefix(work.Class, work.OrgID, work.NodeID)
	var legacy searchPendingWriteRecord
	found, err := readSearchRecord(ctx, tr, legacyKey, &legacy)
	if err != nil || !found {
		return err
	}
	if err := storePendingWrite(ctx, tr, work, legacy.pending()); err != nil {
		return err
	}
	tr.Clear(fdb.Key(legacyKey))
	return nil
}

// storePendingWrite preserves the later original record for one document and index.
// Different identities use different keys and do not replace one another.
func storePendingWrite(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, pending searchdomain.PendingWrite) error {
	key := searchPendingWriteKey(work.Class, work.OrgID, work.NodeID, pending.DocumentID, pending.Index)
	var existing searchPendingWriteRecord
	found, err := readSearchRecord(ctx, tr, key, &existing)
	if err != nil {
		return err
	}
	if found && !pending.Deadline.After(existing.Deadline) {
		return nil
	}
	return writeSearchRecord(ctx, tr, key, searchPendingWriteRecord{
		DocumentID: pending.DocumentID, Index: pending.Index, Ordinal: pending.Ordinal,
		Generation: pending.Generation, AbandonedAt: pending.AbandonedAt, Deadline: pending.Deadline,
	})
}

func (record searchPendingWriteRecord) pending() searchdomain.PendingWrite {
	return searchdomain.PendingWrite{
		DocumentID: record.DocumentID, Index: record.Index, Ordinal: record.Ordinal,
		Generation: record.Generation, AbandonedAt: record.AbandonedAt, Deadline: record.Deadline,
	}
}
