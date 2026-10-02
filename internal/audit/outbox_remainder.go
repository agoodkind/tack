package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// OutboxRemainder counts the operator-command events that wait in
// public.ops_outbox for the relay. Oldest is nil when no event waits.
type OutboxRemainder struct {
	Rows   int64
	Oldest *time.Time
}

// OperatorOutboxRemainder reads the row count and the oldest created_at of
// public.ops_outbox through the ledger reader. The reader may select only the
// event_id and created_at columns (migration 016), never the payloads.
func (r *Reader) OperatorOutboxRemainder(ctx context.Context) (OutboxRemainder, error) {
	if r == nil || r.pool == nil {
		return OutboxRemainder{Rows: 0, Oldest: nil}, fmt.Errorf("audit reader not configured")
	}
	var remainder OutboxRemainder
	err := r.pool.QueryRow(ctx, `SELECT count(event_id), min(created_at) FROM public.ops_outbox`).
		Scan(&remainder.Rows, &remainder.Oldest)
	if err != nil {
		slog.ErrorContext(ctx, "audit.ops_outbox.remainder_failed", slog.String("err", err.Error()))
		return OutboxRemainder{Rows: 0, Oldest: nil}, fmt.Errorf("read the operator outbox remainder: %w", err)
	}
	return remainder, nil
}
