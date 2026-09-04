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

// The dead-letter table is the durable landing zone for every audit event
// the consumer could not commit to audit.events: payloads it could not
// decode, and since TACK-336 payloads whose insert the ledger refused, such as
// a row dated into a week no partition covers. The table has no expiry. A row
// leaves it only when a replay lands the event, and the consumer, as the one
// writer of the ledger, is the one that deletes it. The row key and the replay
// header that names it are in dlq_key.go.

// isDeadLetterable reports whether a projection failure is about the record
// itself, so that retrying the batch could never land it: a payload the
// decoder rejects, or a statement the ledger refused for the record's data
// (SQLSTATE class 22, data exception) or for a constraint it violates (class
// 23, which includes a row dated into a week no partition covers). Every
// other failure, a lost connection, a lock timeout, a permission the role
// lacks, a table that is missing, is about the deployment and must keep
// failing the batch, so the offset stays put and the record is retried once
// the fault is fixed.
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

// deadLetterRecord writes one failed record to the dead-letter table inside
// tx. A first failure inserts the row, and a redelivery of the same record
// (a rebalance, a commit that was lost) leaves the row as it is; only a
// replay that fails again counts the attempt on the original row and
// refreshes its failure text, so attempt_count counts operator replays and
// nothing else.
//
// It runs after the failing statement's savepoint is rolled back, so the
// transaction is live and the dead-letter row commits with the batch's
// offset advance: the record is accounted for exactly once, in the table or
// in the ledger, never in neither.
func deadLetterRecord(ctx context.Context, tx pgx.Tx, rec *kgo.Record, cause error) error {
	key := DeadLetterKey{Topic: rec.Topic, Partition: rec.Partition, Offset: rec.Offset}
	origin, replaying, err := verifiedReplayOrigin(ctx, tx, rec)
	if err != nil {
		return err
	}
	if replaying {
		key = origin
		_, err = tx.Exec(ctx, `
			UPDATE audit.events_dlq
			   SET attempt_count = attempt_count + 1, last_attempt_at = now(), error = $4
			 WHERE topic = $1 AND partition = $2 AND "offset" = $3
		`, key.Topic, key.Partition, key.Offset, cause.Error())
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO audit.events_dlq (received_at, topic, partition, "offset", payload, error, attempt_count, last_attempt_at)
			VALUES (now(), $1, $2, $3, $4, $5, 0, NULL)
			ON CONFLICT (topic, partition, "offset") DO NOTHING
		`, key.Topic, key.Partition, key.Offset, recordPayload(rec), cause.Error())
	}
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.dlq_write_failed",
			slog.String("err", err.Error()),
			slog.String("dead_letter", key.String()),
		)
		return fmt.Errorf("dead-letter %s: %w", key, err)
	}
	slog.ErrorContext(ctx, "audit.consumer.dead_lettered",
		slog.String("dead_letter", key.String()),
		slog.Bool("replay", replaying),
		slog.String("err", cause.Error()),
	)
	return nil
}

// resolveDeadLetter removes the dead-letter row a replayed record came from,
// inside the transaction that landed the event, so the row and the ledger
// row commit or roll back together. A record that is not a replay is a no-op.
func resolveDeadLetter(ctx context.Context, tx pgx.Tx, rec *kgo.Record) error {
	origin, replaying := replayOrigin(ctx, rec)
	if !replaying {
		return nil
	}
	// The row goes only when its stored payload is the bytes that just
	// landed. A header names a row; the payload proves the record is that
	// row's replay, so a producer cannot clear a dead letter it did not carry.
	tag, err := tx.Exec(ctx, `
		DELETE FROM audit.events_dlq
		 WHERE topic = $1 AND partition = $2 AND "offset" = $3 AND payload = $4
	`, origin.Topic, origin.Partition, origin.Offset, recordPayload(rec))
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.dlq_resolve_failed",
			slog.String("err", err.Error()), slog.String("dead_letter", origin.String()))
		return fmt.Errorf("resolve dead-letter %s: %w", origin, err)
	}
	if tag.RowsAffected() == 0 {
		// The named row is gone already (a second replay of it landed first)
		// or its stored payload is not these bytes; either way nothing was
		// resolved by this record.
		slog.WarnContext(ctx, "audit.consumer.dead_letter_not_resolved",
			slog.String("dead_letter", origin.String()), slog.Int64("offset", rec.Offset))
		return nil
	}
	slog.InfoContext(ctx, "audit.consumer.dead_letter_resolved",
		slog.String("dead_letter", origin.String()),
	)
	return nil
}
