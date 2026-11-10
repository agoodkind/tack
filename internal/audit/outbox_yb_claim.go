package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// claimOutboxStatement selects and locks rows in creation order, uses event_id
// to break ties, and skips rows that another transaction has locked.
const claimOutboxStatement = `
	SELECT event_id, event
	  FROM public.ops_outbox
	 ORDER BY created_at ASC, event_id ASC
	 LIMIT $1
	   FOR UPDATE SKIP LOCKED
`

// ClaimBatch locks up to limit outbox rows and calls publish in creation order.
// After each successful call, it deletes the row and its parts and acknowledges
// matching read receipts in the claim transaction.  A publish error stops the
// batch. The transaction commits earlier deletions and leaves the failing row
// and remaining rows for retry. ClaimBatch returns the committed row count and
// wraps the publish error.  A transaction error returns zero. A later claim can
// publish records that Kafka already accepted before the transaction failed.
func (o *PoolOutbox) ClaimBatch(ctx context.Context, limit int, publish func(context.Context, OutboxRow) error) (int, error) {
	published := 0
	var publishErr error
	err := pgx.BeginFunc(ctx, o.pool, func(tx pgx.Tx) error {
		published, publishErr = 0, nil
		rows, err := readOutboxRows(ctx, tx, claimOutboxStatement, limit)
		if err != nil {
			return err
		}
		var deleteErr error
		published, publishErr, deleteErr = publishClaimedRows(ctx, tx, rows, publish)
		return deleteErr
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.outbox.claim_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("claim ops outbox batch: %w", err)
	}
	if publishErr != nil {
		return published, fmt.Errorf("publish claimed ops outbox batch: %w", publishErr)
	}
	return published, nil
}

// publishClaimedRows calls publish for each row in order, then deletes the row
// and its parts and acknowledges matching read receipts in tx. It stops at the
// first failure and returns the completed row count, publish error, and
// database error separately. ClaimBatch commits earlier deletions after a
// publish error and rolls back after a database error.
func publishClaimedRows(ctx context.Context, tx pgx.Tx, rows []OutboxRow, publish func(context.Context, OutboxRow) error) (int, error, error) {
	published := 0
	for _, row := range rows {
		if publishErr := publish(ctx, row); publishErr != nil {
			return published, publishErr, nil
		}
		if err := deletePublishedTx(ctx, tx, row.EventID, rowPublishedReads(ctx, row)); err != nil {
			return published, nil, err
		}
		published++
	}
	return published, nil, nil
}
