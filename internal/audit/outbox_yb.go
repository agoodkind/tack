package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// uniqueViolationSQLState is the Postgres code for a rejected duplicate key.
const uniqueViolationSQLState = "23505"

// OutboxRow is one operator-command event waiting for relay delivery.
type OutboxRow struct {
	// EventID identifies the outbox row.
	EventID uuid.UUID
	// Event is the audit event awaiting relay delivery.
	Event Event
	// Messages contains part messages in part_index order followed by the manifest
	// message for a multipart row. Event remains zero when Messages contains these records.
	Messages [][]byte `exhaustruct:"optional"`
}

// PoolOutbox reads and deletes operator-command events in the YugabyteDB
// outbox. An operator command writes its event into that table inside the same
// transaction as its own work, and the relay drains what this type returns.
type PoolOutbox struct {
	pool *pgxpool.Pool
}

// NewPoolOutbox returns a YugabyteDB outbox accessor over pool.
func NewPoolOutbox(pool *pgxpool.Pool) *PoolOutbox {
	return &PoolOutbox{pool: pool}
}

// Close releases the YugabyteDB pool used by the outbox.
func (o *PoolOutbox) Close() {
	if o != nil && o.pool != nil {
		o.pool.Close()
	}
}

// WriteOutbox writes event to the YugabyteDB operator outbox on its own
// connection, committing by itself.
//
// It is deliberately outside any caller's transaction, despite what this
// type's own comment says about a command writing its event alongside its
// work. That coupling is what WriteOutboxTx offers, and only a command that
// already owns a transaction can take it. The choke-point uses this method,
// because it records an intent before the command runs and an outcome after,
// and neither row may be rolled back by whatever happens in between.
func (o *PoolOutbox) WriteOutbox(ctx context.Context, event Event) error {
	eventJSON, err := marshalOutboxEvent(ctx, event)
	if err != nil {
		return err
	}
	if !FitsSingleRecord(eventJSON) {
		return o.writeLargeOutbox(ctx, event)
	}
	if err := o.insertOutboxEvent(ctx, event, eventJSON); err != nil {
		slog.ErrorContext(ctx, "audit.outbox.write_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write ops outbox event %s: %w", event.EventID, err)
	}
	return nil
}

// WriteOutboxIfAbsent writes event unless an event with its identity already
// exists. It returns true when it inserted a row.
//
// The insert carries no ON CONFLICT clause on purpose. Postgres resolves a
// conflict target by reading the conflicting row, so that clause needs SELECT
// on the table, and an operator command holds INSERT and nothing else exactly
// so it cannot read pending events. The primary key reports the duplicate
// through SQLSTATE 23505 instead, which needs no read: reconstruction stayed
// idempotent while the command kept the narrowest grant it can run with
// (TACK-450).
func (o *PoolOutbox) WriteOutboxIfAbsent(ctx context.Context, event Event) (bool, error) {
	eventJSON, err := marshalOutboxEvent(ctx, event)
	if err != nil {
		return false, err
	}
	if !FitsSingleRecord(eventJSON) {
		err = o.writeLargeOutbox(ctx, event)
	} else {
		_, err = o.pool.Exec(ctx, `
			INSERT INTO public.ops_outbox (event_id, event)
			VALUES ($1, $2)
		`, event.EventID, eventJSON)
	}
	if isOutboxDuplicate(err) {
		return false, nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "audit.outbox.idempotent_write_failed",
			slog.String("event_id", event.EventID.String()),
			slog.String("err", err.Error()),
		)
		return false, fmt.Errorf("write idempotent ops outbox event %s: %w", event.EventID, err)
	}
	return true, nil
}

// isOutboxDuplicate reports whether err is the primary key rejecting an event
// identity the outbox already holds.
func isOutboxDuplicate(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == uniqueViolationSQLState
}

// WriteOutboxTx writes event to the YugabyteDB operator outbox in tx.
func (o *PoolOutbox) WriteOutboxTx(ctx context.Context, tx pgx.Tx, event Event) error {
	eventJSON, err := marshalOutboxEvent(ctx, event)
	if err != nil {
		return err
	}
	if FitsSingleRecord(eventJSON) {
		return insertOutboxRow(ctx, tx, event.EventID, eventJSON)
	}
	encoded, err := EncodeEvent(event, nil)
	if err != nil {
		return err
	}
	return WriteEncodedOutboxTx(ctx, tx, encoded)
}

func marshalOutboxEvent(ctx context.Context, event Event) ([]byte, error) {
	eventJSON, err := json.Marshal(event)
	if err != nil {
		slog.ErrorContext(ctx, "audit.outbox.event_encode_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("encode ops outbox event: %w", err)
	}
	return eventJSON, nil
}

// ReadBatch reads up to limit events in creation order. The event id breaks
// ties: now() is stable within a transaction, so two rows can share a
// creation time, and without the tie-break the drain order would vary between
// runs over the same data.
func (o *PoolOutbox) ReadBatch(ctx context.Context, limit int) ([]OutboxRow, error) {
	rows, err := o.pool.Query(ctx, `
		SELECT event_id, event
		  FROM public.ops_outbox
		 ORDER BY created_at ASC, event_id ASC
		 LIMIT $1
	`, limit)
	if err != nil {
		slog.ErrorContext(ctx, "audit.outbox.read_query_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read ops outbox: %w", err)
	}
	defer rows.Close()

	out := make([]OutboxRow, 0)
	for rows.Next() {
		var row OutboxRow
		var eventJSON []byte
		if err := rows.Scan(&row.EventID, &eventJSON); err != nil {
			slog.ErrorContext(ctx, "audit.outbox.read_scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("scan ops outbox row: %w", err)
		}
		header, isManifest, err := manifestHeader(eventJSON)
		if err != nil {
			slog.ErrorContext(ctx, "audit.outbox.manifest_decode_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("decode ops outbox manifest %s: %w", row.EventID, err)
		}
		if isManifest {
			row.Messages = [][]byte{header}
		} else if err := json.Unmarshal(eventJSON, &row.Event); err != nil {
			slog.ErrorContext(ctx, "audit.outbox.event_decode_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("decode ops outbox event: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.outbox.read_rows_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read ops outbox rows: %w", err)
	}
	rows.Close()
	for position, row := range out {
		if len(row.Messages) == 0 {
			continue
		}
		messages, err := o.outboxPartMessages(ctx, row.EventID, row.Messages[0])
		if err != nil {
			return nil, err
		}
		out[position].Messages = messages
	}
	return out, nil
}
