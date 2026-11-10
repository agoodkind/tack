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
	// Messages stores multipart messages in part_index order, followed by the
	// manifest. Event retains its zero value when Messages stores these records.
	Messages [][]byte `exhaustruct:"optional"`
}

// PoolOutbox reads and deletes operator-command events in the YugabyteDB
// outbox. WriteOutboxTx records an event in the transaction for the command's
// work. WriteOutbox commits independently. The relay processes returned rows.
type PoolOutbox struct {
	pool *pgxpool.Pool
}

// NewPoolOutbox constructs a YugabyteDB outbox accessor using pool.
//
// The accessor uses the supplied pool directly without creating another pool.
//
//   - Closing the accessor also closes the supplied pool.
func NewPoolOutbox(pool *pgxpool.Pool) *PoolOutbox {
	return &PoolOutbox{pool: pool}
}

// Close closes the YugabyteDB pool that the outbox uses.
//
// The method checks both the receiver and pool before closing the pool.
//
//   - A nil receiver does not close a pool.
//   - A receiver with a nil pool does not close a pool.
func (o *PoolOutbox) Close() {
	if o != nil && o.pool != nil {
		o.pool.Close()
	}
}

// WriteOutbox writes event to the YugabyteDB operator outbox on its own
// connection and commits independently of the caller's transaction.
//
// WriteOutboxTx records an event alongside the command's work when the caller
// already owns a transaction. The shared command audit recorder uses
// WriteOutbox to prevent a command rollback from undoing its audit records.
//
//   - The recorder writes intent before the command runs.
//   - The recorder writes the outcome after the command runs.
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

// WriteOutboxIfAbsent writes event unless the outbox already stores that
// event identity.
//
// The insert intentionally omits ON CONFLICT. PostgreSQL reads the conflicting
// row to resolve a conflict target, which requires SELECT. Operator commands
// have only INSERT and cannot read pending events. The primary key reports a
// duplicate as SQLSTATE 23505 without requiring a read. TACK-450 preserved
// idempotent reconstruction under this minimum grant.
//
//   - A new event identity returns true after a successful insert.
//   - A duplicate event identity returns false without an error.
func (o *PoolOutbox) WriteOutboxIfAbsent(ctx context.Context, event Event) (bool, error) {
	eventJSON, err := marshalOutboxEvent(ctx, event)
	if err != nil {
		return false, err
	}

	if !FitsSingleRecord(eventJSON) {
		err = o.writeLargeOutbox(ctx, event)
	} else {
		_, err = o.pool.Exec(
			ctx,
			`
			INSERT INTO public.ops_outbox (event_id, event)
			VALUES ($1, $2)
		`,
			event.EventID,
			eventJSON,
		)
	}

	if isOutboxDuplicate(err) {
		return false, nil
	}

	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.outbox.idempotent_write_failed",
			slog.String("event_id", event.EventID.String()),
			slog.String("err", err.Error()),
		)

		return false, fmt.Errorf("write idempotent ops outbox event %s: %w", event.EventID, err)
	}

	return true, nil
}

// isOutboxDuplicate reports whether err indicates that the outbox already
// stores an event identity.
func isOutboxDuplicate(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == uniqueViolationSQLState
}

// WriteOutboxTx writes event to the YugabyteDB operator outbox in tx.
//
// The caller controls the transaction. The event size determines the stored
// representation.
//
//   - An event that fits one record uses a plain outbox row.
//   - A larger event uses an encoded header and ordered parts.
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

// ReadBatch reads up to limit events in creation order.
//
// The event ID determines the order of rows with equal creation times.
// now() stays constant within a transaction, and rows can share a creation
// time. Without the event ID comparison, repeated reads of unchanged data
// could return different orders.
//
//   - Rows with equal created_at values sort by event_id.
func (o *PoolOutbox) ReadBatch(ctx context.Context, limit int) ([]OutboxRow, error) {
	var batch []OutboxRow
	err := pgx.BeginTxFunc(
		ctx,
		o.pool,
		pgx.TxOptions{AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			var readErr error
			batch, readErr = readOutboxRows(
				ctx,
				tx,
				`
			SELECT event_id, event
			  FROM public.ops_outbox
			 ORDER BY created_at ASC, event_id ASC
			 LIMIT $1
		`,
				limit,
			)

			return readErr
		},
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.outbox.read_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read ops outbox batch: %w", err)
	}

	return batch, nil
}

// readOutboxRows executes statement with limit in tx and decodes the selected
// outbox rows. For multipart events, it reads parts in part_index order and
// appends the manifest.
func readOutboxRows(
	ctx context.Context,
	tx pgx.Tx,
	statement string,
	limit int,
) ([]OutboxRow, error) {
	rows, err := tx.Query(ctx, statement, limit)
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
			slog.ErrorContext(
				ctx,
				"audit.outbox.manifest_decode_failed",
				slog.String("err", err.Error()),
			)

			return nil, fmt.Errorf("decode ops outbox manifest %s: %w", row.EventID, err)
		}

		if isManifest {
			row.Messages = [][]byte{header}
		} else if err := json.Unmarshal(eventJSON, &row.Event); err != nil {
			slog.ErrorContext(
				ctx,
				"audit.outbox.event_decode_failed",
				slog.String("err", err.Error()),
			)

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

		messages, err := outboxPartMessages(ctx, tx, row.EventID, row.Messages[0])
		if err != nil {
			return nil, err
		}

		out[position].Messages = messages
	}

	return out, nil
}
