package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// OutboxRemainder counts operator-command events awaiting relay delivery in
// public.ops_outbox. Oldest is nil when the outbox does not contain any events.
type OutboxRemainder struct {
	Rows   int64
	Oldest *time.Time
}

// OperatorOutboxRemainder reads the row count and oldest created_at from
// public.ops_outbox through the ledger reader.
//
// Migration 016 permits the reader to select only the columns needed to count
// and age pending events. The reader cannot select event payloads.
//
//   - event_id supplies the row count.
//   - created_at supplies the oldest creation time.
func (r *Reader) OperatorOutboxRemainder(ctx context.Context) (OutboxRemainder, error) {
	if r == nil || r.pool == nil {
		return OutboxRemainder{Rows: 0, Oldest: nil}, fmt.Errorf("audit reader not configured")
	}
	var remainder OutboxRemainder
	err := r.pool.Scan(r.pool.unsanitized.QueryRow(ctx, `SELECT count(event_id), min(created_at) FROM public.ops_outbox`).
		Scan(&remainder.Rows, &remainder.Oldest))
	if err != nil {
		slog.ErrorContext(ctx, "audit.ops_outbox.remainder_failed", slog.String("err", err.Error()))
		return OutboxRemainder{Rows: 0, Oldest: nil}, fmt.Errorf("read the operator outbox remainder: %w", err)
	}
	return remainder, nil
}
