package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/twmb/franz-go/pkg/kgo"
)

// The dead-letter table durably stores audit payloads the consumer cannot
// decode. Since TACK-336, it also stores payloads the ledger rejects, including
// a row dated into a week without a partition. The table does not expire rows.
//
// The consumer is the ledger's only writer. It deletes a dead-letter row after
// a replay commits the event. [DeadLetterKey] and dlqReplayHeader identify the
// original row.

// isDeadLetterable reports whether the payload or its data caused a projection
// failure that a batch retry cannot repair. It accepts decoder failures,
// SQLSTATE class 22 data exceptions, and class 23 constraint failures,
// including a row dated into a week without a partition.
//
// Other failures must fail the batch. Connection loss, lock timeouts, missing
// permissions, and missing tables preserve the offset for a retry after repair.
func isDeadLetterable(err error) bool {
	if errors.Is(err, errMalformedPayload) {
		return true
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}

	class := ""
	if len(pgErr.Code) >= 2 {
		class = pgErr.Code[:2]
	}

	return class == "22" || class == "23"
}

// DeadLetter is one row of the dead-letter table as an operator reads it.
type DeadLetter struct {
	Key           DeadLetterKey
	ReceivedAt    time.Time
	Payload       []byte
	Error         string
	AttemptCount  int
	LastAttemptAt *time.Time
}

// DeadLetterSummary counts the rows sharing one failure text.
type DeadLetterSummary struct {
	Error  string
	Count  int
	Oldest time.Time
	Newest time.Time
}

// deadLetterRecord writes a failed record to the dead-letter table inside tx.
// An ordinary failure uses the record's topic, partition, and offset as its
// key. A first failure inserts the row. Redelivery after a rebalance or lost
// commit preserves that row. Only a verified replay failure increments the
// original row's attempt_count and updates its failure text. The count records
// operator replays rather than ordinary redelivery.
//
// projectRecord rolls back the failed statement's savepoint before this write.
// projectBatchOnce commits the dead-letter change and consumer offset in one
// transaction. The Kafka-position conflict key prevents duplicate ordinary
// dead-letter rows. The event-ID claim separately prevents duplicate ledger
// insertion.
//
// Valid multipart messages can persist in durable assembly records and commit
// their consumer offset before the complete event exists. Those assembly
// records are separate from ledger and dead-letter rows.
func deadLetterRecord(ctx context.Context, tx pgx.Tx, rec *kgo.Record, cause error) error {
	key := DeadLetterKey{Topic: rec.Topic, Partition: rec.Partition, Offset: rec.Offset}
	origin, replaying, err := verifiedReplayOrigin(ctx, tx, rec)
	if err != nil {
		return err
	}

	if replaying {
		var tag pgconn.CommandTag
		tag, err = tx.Exec(
			ctx,
			`
			UPDATE audit.events_dlq
			   SET attempt_count = attempt_count + 1, last_attempt_at = now(), error = $4
			 WHERE topic = $1 AND partition = $2 AND "offset" = $3
		`,
			origin.Topic,
			origin.Partition,
			origin.Offset,
			cause.Error(),
		)
		if err == nil && tag.RowsAffected() == 0 {
			// An operator may delete the verified row between the check and this update.
			// Store the failed replay under its own Kafka key when the update cannot find
			// the original row.
			slog.WarnContext(
				ctx,
				"audit.consumer.dlq_replay_origin_gone",
				slog.String("dead_letter", origin.String()),
				slog.Int64("offset", rec.Offset),
			)
			replaying = false
		} else {
			key = origin
		}
	}

	if !replaying {
		_, err = tx.Exec(
			ctx,
			`
			INSERT INTO audit.events_dlq (received_at, topic, partition, "offset", payload, error, attempt_count, last_attempt_at)
			VALUES (now(), $1, $2, $3, $4, $5, 0, NULL)
			ON CONFLICT (topic, partition, "offset") DO NOTHING
		`,
			key.Topic,
			key.Partition,
			key.Offset,
			recordPayload(rec),
			cause.Error(),
		)
	}

	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.consumer.dlq_write_failed",
			slog.String("err", err.Error()),
			slog.String("dead_letter", key.String()),
		)

		return fmt.Errorf("dead-letter %s: %w", key, err)
	}

	slog.ErrorContext(
		ctx,
		"audit.consumer.dead_lettered",
		slog.String("dead_letter", key.String()),
		slog.Bool("replay", replaying),
		slog.String("err", cause.Error()),
	)

	return nil
}

// resolveDeadLetter removes a replay's original dead-letter row inside the
// transaction that commits the event. The deletion and ledger insertion commit
// or roll back together. The function returns without a change for other
// records.
func resolveDeadLetter(ctx context.Context, tx pgx.Tx, rec *kgo.Record) error {
	origin, replaying := replayOrigin(ctx, rec)
	if !replaying {
		return nil
	}

	// The header identifies the original row. Delete it only when its payload
	// equals the committed replay bytes. A producer cannot delete an unrelated dead
	// letter by changing the header.
	tag, err := tx.Exec(
		ctx,
		`
		DELETE FROM audit.events_dlq
		 WHERE topic = $1 AND partition = $2 AND "offset" = $3 AND payload = $4
	`,
		origin.Topic,
		origin.Partition,
		origin.Offset,
		recordPayload(rec),
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.consumer.dlq_resolve_failed",
			slog.String("err", err.Error()),
			slog.String("dead_letter", origin.String()),
		)

		return fmt.Errorf("resolve dead-letter %s: %w", origin, err)
	}

	if tag.RowsAffected() == 0 {
		// The row may already have disappeared after another successful replay, or its
		// stored payload may differ. This record does not resolve a row in either case.
		slog.WarnContext(
			ctx,
			"audit.consumer.dead_letter_not_resolved",
			slog.String("dead_letter", origin.String()),
			slog.Int64("offset", rec.Offset),
		)

		return nil
	}

	slog.InfoContext(
		ctx,
		"audit.consumer.dead_letter_resolved",
		slog.String("dead_letter", origin.String()),
	)

	return nil
}
