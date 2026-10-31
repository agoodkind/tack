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
// The key includes the work class, organization, node, document ID, and index.
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

func searchPendingWritePrefix(class searchdomain.WorkClass, orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchPendingWrite, string(class), orgID.String(), nodeID.String()}.Pack())
}

// searchPendingWriteKey identifies one document's request to one physical index.
func searchPendingWriteKey(class searchdomain.WorkClass, orgID, nodeID uuid.UUID, documentID, index string) []byte {
	return withPrefix(tuple.Tuple{keySearchPendingWrite, string(class), orgID.String(), nodeID.String(), documentID, index}.Pack())
}

// recordPendingWrite adopts deployed per-node state before preserving pending.
// It retains the later original deadline for the same document and index.
func recordPendingWrite(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, pending searchdomain.PendingWrite) error {
	if err := adoptPendingWrite(ctx, tr, work); err != nil {
		return err
	}
	return storePendingWrite(ctx, tr, work, pending)
}

// clearPendingWrite clears only the completed document/index identity.
func clearPendingWrite(tr fdb.Transaction, class searchdomain.WorkClass, orgID, nodeID uuid.UUID, documentID, index string) {
	tr.Clear(fdb.Key(searchPendingWriteKey(class, orgID, nodeID, documentID, index)))
}

// Clear both deployed and document/index keys when their owning work is removed.
func clearPendingWrites(tr fdb.Transaction, class searchdomain.WorkClass, orgID, nodeID uuid.UUID) {
	prefix := searchPendingWritePrefix(class, orgID, nodeID)
	end := append(append([]byte(nil), prefix...), 0xFF)
	tr.ClearRange(fdb.KeyRange{Begin: fdb.Key(prefix), End: fdb.Key(end)})
}

// checkPendingWrite reads only documentID in work.Target. It returns the record
// as held before its deadline and otherwise clears and returns it as expired.
// It does not read or clear deadlines for another document or physical index.
func checkPendingWrite(ctx context.Context, tr fdb.Transaction, now time.Time, work searchdomain.Work, documentID string) (pendingWriteCheck, error) {
	none := pendingWriteCheck{held: nil, expired: nil}
	var record searchPendingWriteRecord
	found, err := readSearchRecord(ctx, tr, searchPendingWriteKey(work.Class, work.OrgID, work.NodeID, documentID, work.Target), &record)
	if err != nil || !found {
		return none, err
	}
	pending := record.pending()
	if now.Before(record.Deadline) {
		return pendingWriteCheck{held: &pending, expired: nil}, nil
	}
	clearPendingWrite(tr, work.Class, work.OrgID, work.NodeID, documentID, work.Target)
	return pendingWriteCheck{held: nil, expired: &pending}, nil
}

// heldMirrorWrite adopts deployed copy state and reads the live and copy deadlines for
// documentID in work.Mirror. It returns the later unexpired original record.
// A resumed live claim writes the serving target and schedules the owed copy
// without clearing the live deadline merely because the target differs.
func heldMirrorWrite(ctx context.Context, tr fdb.Transaction, now time.Time, work searchdomain.Work, documentID string) (*searchdomain.PendingWrite, error) {
	if work.Mirror == "" {
		return nil, nil
	}
	copyWork := work
	copyWork.Class = searchdomain.WorkClassCopy
	if err := adoptPendingWrite(ctx, tr, copyWork); err != nil {
		return nil, err
	}
	var held *searchdomain.PendingWrite
	for _, class := range []searchdomain.WorkClass{work.Class, searchdomain.WorkClassCopy} {
		var record searchPendingWriteRecord
		found, err := readSearchRecord(ctx, tr, searchPendingWriteKey(class, work.OrgID, work.NodeID, documentID, work.Mirror), &record)
		if err != nil {
			return nil, err
		}
		if !found || !now.Before(record.Deadline) {
			continue
		}
		if held == nil || record.Deadline.After(held.Deadline) {
			pending := record.pending()
			held = &pending
		}
	}
	return held, nil
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
