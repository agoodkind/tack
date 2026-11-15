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

// The dead-letter table durably stores undecodable payloads and record-specific
// insert failures, including missing weekly partitions (TACK-336). Rows do not
// expire. The ledger consumer deletes them after successful replay.

// isDeadLetterable accepts malformed payloads, data exceptions (SQLSTATE class
// 22), and constraint violations (class 23). Connection, lock, permission, and
// schema failures must fail the batch without advancing its offset for retry
// after deployment recovery.
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

// deadLetterRecord records failure after rolling back the record savepoint.
// Its row commits with the batch offset. Redelivery preserves the existing
// row; only verified operator replays increment attempts and refresh error
// text.
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
			// Preserve the failed replay under its own key if the verified
			// original row disappeared between verification and update.
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

// resolveDeadLetter deletes the replay origin in the ledger transaction.
// Non-replay records do not change the dead-letter table.
func resolveDeadLetter(ctx context.Context, tx pgx.Tx, rec *kgo.Record) error {
	origin, replaying := replayOrigin(ctx, rec)
	if !replaying {
		return nil
	}

	// Require payload equality as well as the header key. A producer must not
	// delete a dead letter with unrelated bytes.
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
		// Another replay already deleted the row, or its payload differs.
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
