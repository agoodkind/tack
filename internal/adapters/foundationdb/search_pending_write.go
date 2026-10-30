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

// searchPendingWriteRecord stores one abandoned page write.
// The key includes the work class, organization, node, and document ID.
// The key does not include the generation.
// Claims of newer generations of the same document read the same record.
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

// searchPendingWritePrefix returns the prefix for all pending write records
// for class, orgID, and nodeID.
func searchPendingWritePrefix(class searchdomain.WorkClass, orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchPendingWrite, string(class), orgID.String(), nodeID.String()}.Pack())
}

// searchPendingWriteKey returns the key for documentID under the pending write
// prefix for class, orgID, and nodeID.
func searchPendingWriteKey(class searchdomain.WorkClass, orgID, nodeID uuid.UUID, documentID string) []byte {
	return withPrefix(tuple.Tuple{keySearchPendingWrite, string(class), orgID.String(), nodeID.String(), documentID}.Pack())
}

// recordPendingWrite stores pending under the class, organization, and node of work
// and the document ID of pending. It does not change records for other documents.
func recordPendingWrite(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, pending searchdomain.PendingWrite) error {
	return writeSearchRecord(ctx, tr, searchPendingWriteKey(work.Class, work.OrgID, work.NodeID, pending.DocumentID), searchPendingWriteRecord{
		DocumentID: pending.DocumentID, Index: pending.Index, Ordinal: pending.Ordinal,
		Generation: pending.Generation, AbandonedAt: pending.AbandonedAt, Deadline: pending.Deadline,
	})
}

// clearPendingWrite clears the record for documentID in class, orgID, and nodeID.
func clearPendingWrite(tr fdb.Transaction, class searchdomain.WorkClass, orgID, nodeID uuid.UUID, documentID string) {
	tr.Clear(fdb.Key(searchPendingWriteKey(class, orgID, nodeID, documentID)))
}

// clearPendingWrites clears all pending write records for class, orgID, and nodeID
// with one range clear. clearSearchWork, the node deletion path, the schedule
// removal plan, and dropSearchWork use clearPendingWrites.
func clearPendingWrites(tr fdb.Transaction, class searchdomain.WorkClass, orgID, nodeID uuid.UUID) {
	prefix := searchPendingWritePrefix(class, orgID, nodeID)
	end := append(append([]byte(nil), prefix...), 0xFF)
	tr.ClearRange(fdb.KeyRange{Begin: fdb.Key(prefix), End: fdb.Key(end)})
}

// checkPendingWrite reads only the pending write record for documentID.
// It returns the record as held when the record index equals work.Target
// and now precedes the deadline. Otherwise, it clears that record.
// It returns the cleared record as expired only when the record index equals work.Target.
// It never reads or clears records for other documents.
func checkPendingWrite(ctx context.Context, tr fdb.Transaction, now time.Time, work searchdomain.Work, documentID string) (pendingWriteCheck, error) {
	none := pendingWriteCheck{held: nil, expired: nil}
	var record searchPendingWriteRecord
	found, err := readSearchRecord(ctx, tr, searchPendingWriteKey(work.Class, work.OrgID, work.NodeID, documentID), &record)
	if err != nil || !found {
		return none, err
	}
	pending := searchdomain.PendingWrite{
		DocumentID: record.DocumentID, Index: record.Index, Ordinal: record.Ordinal,
		Generation: record.Generation, AbandonedAt: record.AbandonedAt, Deadline: record.Deadline,
	}
	writtenIndex := record.Index == work.Target
	if writtenIndex && now.Before(record.Deadline) {
		return pendingWriteCheck{held: &pending, expired: nil}, nil
	}
	clearPendingWrite(tr, work.Class, work.OrgID, work.NodeID, documentID)
	if writtenIndex {
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
