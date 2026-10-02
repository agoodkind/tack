package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
		return OutboxRemainder{Rows: 0, Oldest: nil}, errors.New("audit reader not configured")
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

// OperatorOutboxEventIDs reads the event_id of every event that waits in
// public.ops_outbox for the relay, through the ledger reader.
func (r *Reader) OperatorOutboxEventIDs(ctx context.Context) ([]uuid.UUID, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("audit reader not configured")
	}
	rows, err := r.pool.Query(ctx, `SELECT event_id FROM public.ops_outbox`)
	if err != nil {
		slog.ErrorContext(ctx, "audit.ops_outbox.event_ids_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read the operator outbox event IDs: %w", err)
	}
	eventIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		slog.ErrorContext(ctx, "audit.ops_outbox.event_ids_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read the operator outbox event IDs: %w", err)
	}
	return eventIDs, nil
}

// OperatorOutboxWaiting counts the events of eventIDs that still wait in
// public.ops_outbox for the relay, through the ledger reader.
func (r *Reader) OperatorOutboxWaiting(ctx context.Context, eventIDs []uuid.UUID) (int64, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("audit reader not configured")
	}
	if len(eventIDs) == 0 {
		return 0, nil
	}
	var waiting int64
	err := r.pool.QueryRow(ctx, `SELECT count(event_id) FROM public.ops_outbox WHERE event_id = ANY($1::uuid[])`,
		uuidStrings(eventIDs)).Scan(&waiting)
	if err != nil {
		slog.ErrorContext(ctx, "audit.ops_outbox.waiting_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("count the waiting operator outbox events: %w", err)
	}
	return waiting, nil
}
