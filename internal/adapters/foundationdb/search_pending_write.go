package foundationdb

import (
	"context"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// searchPendingWriteRecord records one abandoned page write per work class
// and node. Its key does not include the generation, and claims of newer
// generations read the same record.
type searchPendingWriteRecord struct {
	DocumentID  string    `json:"document_id"`
	Index       string    `json:"index"`
	Ordinal     uint64    `json:"ordinal"`
	Generation  int64     `json:"generation"`
	AbandonedAt time.Time `json:"abandoned_at"`
	Deadline    time.Time `json:"deadline"`
}

// pendingWriteCheck contains the pending write found during page registration.
type pendingWriteCheck struct {
	// held is the matching document and index record while its deadline has
	// not passed.
	held *searchdomain.PendingWrite
	// expired is the matching document and index record that registration
	// clears at or after its deadline.
	expired *searchdomain.PendingWrite
}

func searchPendingWriteKey(class string, orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchPendingWrite, class, orgID.String(), nodeID.String()}.Pack())
}

// recordPendingWrite stores pending for the class and node of work in tr.
func recordPendingWrite(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, pending searchdomain.PendingWrite) error {
	return writeSearchRecord(ctx, tr, searchPendingWriteKey(string(work.Class), work.OrgID, work.NodeID), searchPendingWriteRecord{
		DocumentID: pending.DocumentID, Index: pending.Index, Ordinal: pending.Ordinal,
		Generation: pending.Generation, AbandonedAt: pending.AbandonedAt, Deadline: pending.Deadline,
	})
}

// clearPendingWrite removes the pending write record for one class and node.
func clearPendingWrite(tr fdb.Transaction, class searchdomain.WorkClass, orgID, nodeID uuid.UUID) {
	tr.Clear(fdb.Key(searchPendingWriteKey(string(class), orgID, nodeID)))
}

// checkPendingWrite reads the pending write for the class and node of work.
// It returns held when the document ID matches, the index is the target or
// mirror, and now precedes the deadline. It clears any other record. It
// returns the cleared record as expired only when the document and index
// match and now is at or after the deadline.
func checkPendingWrite(ctx context.Context, tr fdb.Transaction, now time.Time, work searchdomain.Work, documentID string) (pendingWriteCheck, error) {
	none := pendingWriteCheck{held: nil, expired: nil}
	var record searchPendingWriteRecord
	found, err := readSearchRecord(ctx, tr, searchPendingWriteKey(string(work.Class), work.OrgID, work.NodeID), &record)
	if err != nil || !found {
		return none, err
	}
	pending := searchdomain.PendingWrite{
		DocumentID: record.DocumentID, Index: record.Index, Ordinal: record.Ordinal,
		Generation: record.Generation, AbandonedAt: record.AbandonedAt, Deadline: record.Deadline,
	}
	sameDocument := record.DocumentID == documentID
	writtenIndex := record.Index == work.Target || record.Index == work.Mirror
	if sameDocument && writtenIndex && now.Before(record.Deadline) {
		return pendingWriteCheck{held: &pending, expired: nil}, nil
	}
	clearPendingWrite(tr, work.Class, work.OrgID, work.NodeID)
	if sameDocument && writtenIndex {
		return pendingWriteCheck{held: nil, expired: &pending}, nil
	}
	return none, nil
}

// logExpiredPendingWrite logs an expired pending write after Register clears
// its record and commits. The worker may then send the page again.
// It does not log when pending is nil.
func logExpiredPendingWrite(ctx context.Context, now time.Time, work searchdomain.Work, pending *searchdomain.PendingWrite) {
	if pending == nil {
		return
	}
	telemetry.L(ctx).InfoContext(ctx, "search.work.pending_write_expired", slog.String("node_id", work.NodeID.String()),
		slog.String("class", string(work.Class)), slog.Int64("generation", work.Generation),
		slog.Int64("abandoned_generation", pending.Generation), slog.String("document_id", pending.DocumentID),
		slog.String("index", pending.Index), slog.Uint64("ordinal", pending.Ordinal),
		slog.Duration("since_abandoned", now.Sub(pending.AbandonedAt)))
}
