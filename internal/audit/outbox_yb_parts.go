package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// WriteEncodedOutboxTx calls public.ops_outbox_write_encoded inside the caller's
// transaction with the event ID, plain event or manifest, and ordered part messages.
func WriteEncodedOutboxTx(ctx context.Context, tx pgx.Tx, encoded EncodedEvent) error {
	event, parts, err := encoded.HeaderAndParts()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT public.ops_outbox_write_encoded($1, $2, $3)`, encoded.EventID, event, parts); err != nil {
		slog.ErrorContext(ctx, "audit.outbox.encoded_write_failed", slog.String("event_id", encoded.EventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("write ops outbox event %s with %d parts: %w", encoded.EventID, len(parts), err)
	}
	return nil
}

// writeLargeOutbox encodes an event and calls WriteEncodedOutboxTx
// in a pool transaction that it opens and commits.
func (o *PoolOutbox) writeLargeOutbox(ctx context.Context, event Event) error {
	encoded, err := EncodeEvent(event, nil)
	if err != nil {
		return err
	}
	tx, err := o.pool.Begin(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.outbox.begin_failed", slog.String("err", err.Error()))
		return fmt.Errorf("begin ops outbox write of event %s: %w", event.EventID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := WriteEncodedOutboxTx(ctx, tx, encoded); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		slog.ErrorContext(ctx, "audit.outbox.commit_failed", slog.String("err", err.Error()))
		return fmt.Errorf("commit ops outbox write of event %s: %w", event.EventID, err)
	}
	return nil
}

// insertOutboxRow inserts one plain event row inside the caller's transaction.
func insertOutboxRow(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, eventJSON []byte) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.ops_outbox (event_id, event)
		VALUES ($1, $2)
	`, eventID, eventJSON); err != nil {
		slog.ErrorContext(ctx, "audit.outbox.write_tx_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write ops outbox event %s in transaction: %w", eventID, err)
	}
	return nil
}

// outboxPartMessages reads an event's part messages in part_index order
// and appends the manifest header.
func (o *PoolOutbox) outboxPartMessages(ctx context.Context, eventID uuid.UUID, header []byte) ([][]byte, error) {
	rows, err := o.pool.Query(ctx, `
		SELECT message FROM public.ops_outbox_parts
		 WHERE event_id = $1
		 ORDER BY part_index ASC
	`, eventID)
	if err != nil {
		slog.ErrorContext(ctx, "audit.outbox.parts_query_failed", slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("read ops outbox event %s parts: %w", eventID, err)
	}
	messages, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		slog.ErrorContext(ctx, "audit.outbox.parts_scan_failed", slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("scan ops outbox event %s parts: %w", eventID, err)
	}
	return append(messages, header), nil
}

// Delete removes an event's parts and outbox row in one transaction.
// The relay runs Delete after the broker accepts every record for the row.
func (o *PoolOutbox) Delete(ctx context.Context, eventID uuid.UUID) error {
	return o.DeletePublished(ctx, eventID, nil)
}

// DeletePublished deletes an event's parts and outbox row and marks the read
// receipts in reads as published in the same transaction.
func (o *PoolOutbox) DeletePublished(ctx context.Context, eventID uuid.UUID, reads []PublishedRead) error {
	err := pgx.BeginFunc(ctx, o.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM public.ops_outbox_parts WHERE event_id = $1`, eventID); err != nil {
			return fmt.Errorf("delete parts: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM public.ops_outbox WHERE event_id = $1`, eventID); err != nil {
			return fmt.Errorf("delete event: %w", err)
		}
		for _, read := range reads {
			if _, err := tx.Exec(ctx, readStatement("acknowledge"), read.EventID, read.Digest); err != nil {
				return fmt.Errorf("mark read %s published: %w", read.EventID, err)
			}
		}
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.outbox.delete_failed",
			slog.String("event_id", eventID.String()),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("delete ops outbox event %s: %w", eventID, err)
	}
	return nil
}

// manifestHeader decodes stored jsonb and re-encodes a manifest message.
// It rejects a stored part message and returns false for a plain event.
func manifestHeader(eventJSON []byte) ([]byte, bool, error) {
	message, isTransport, err := decodeTransportMessage(eventJSON)
	if err != nil || !isTransport {
		return nil, isTransport, err
	}
	if message.Message != messageKindManifest {
		return nil, true, errors.New("ops outbox row contains a part message instead of a manifest message")
	}
	header, err := encodeTransportMessage(message)
	return header, true, err
}
