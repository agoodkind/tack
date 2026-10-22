package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// ClearThroughPublished deletes every outbox entry up to and including mark
// and marks each read receipt in reads published, in one transaction. The
// relay runs it after the broker acknowledges those entries.
func (s *OpsOutboxStore) ClearThroughPublished(ctx context.Context, mark []byte, reads []audit.PublishedRead) (err error) {
	defer telemetry.FDBOp(ctx, "store.ops_outbox.clear_through_published")(&err)
	if len(mark) == 0 {
		return nil
	}
	if err := checkOutboxMark(ctx, mark); err != nil {
		return err
	}
	end, err := fdb.Strinc(mark)
	if err != nil {
		return auditReadFailure(ctx, "find the end of the delivered outbox entries", uuid.Nil, err)
	}
	clearRange := fdb.KeyRange{Begin: fdb.Key(opsOutboxPrefix()), End: fdb.Key(end)}
	err = transactAuditRead(ctx, s.db, func(tr fdb.Transaction) error {
		tr.ClearRange(clearRange)
		return acknowledgeReads(tr, processClock{}.Now(), reads)
	})
	if err != nil {
		return auditReadFailure(ctx, "clear delivered outbox entries", uuid.Nil, err)
	}
	return nil
}
