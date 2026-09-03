package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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
// writer of the ledger, is the one that deletes it.

// dlqReplayHeader names the Kafka record header a replay carries. Its value
// is the dead-letter row's key, topic/partition/offset of the original
// record, so the consumer can resolve the row the replay came from whatever
// offset the replay itself lands on.
const dlqReplayHeader = "tack-dlq-replay"

// DeadLetterKey identifies one dead-letter row by the Kafka position of the
// record that produced it.
type DeadLetterKey struct {
	Topic     string
	Partition int32
	Offset    int64
}

// String renders the key in the shape the replay header carries.
func (k DeadLetterKey) String() string {
	return k.Topic + "/" + strconv.FormatInt(int64(k.Partition), 10) + "/" + strconv.FormatInt(k.Offset, 10)
}

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

// noDeadLetter is the zero key, returned where no row is named.
var noDeadLetter = DeadLetterKey{Topic: "", Partition: 0, Offset: 0}

// parseDeadLetterKey reads a key back out of a replay header value.
func parseDeadLetterKey(ctx context.Context, value string) (DeadLetterKey, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 3 {
		err := fmt.Errorf("dead-letter key %q: want topic/partition/offset", value)
		slog.WarnContext(ctx, "audit.consumer.dlq_replay_header_invalid", slog.String("err", err.Error()))
		return noDeadLetter, err
	}
	partition, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil {
		slog.WarnContext(ctx, "audit.consumer.dlq_replay_header_invalid", slog.String("err", err.Error()))
		return noDeadLetter, fmt.Errorf("dead-letter key %q: partition: %w", value, err)
	}
	offset, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		slog.WarnContext(ctx, "audit.consumer.dlq_replay_header_invalid", slog.String("err", err.Error()))
		return noDeadLetter, fmt.Errorf("dead-letter key %q: offset: %w", value, err)
	}
	return DeadLetterKey{Topic: parts[0], Partition: int32(partition), Offset: offset}, nil
}

// replayOrigin returns the dead-letter row a record is replaying, when the
// record carries the replay header.
func replayOrigin(ctx context.Context, rec *kgo.Record) (DeadLetterKey, bool, error) {
	for _, header := range rec.Headers {
		if header.Key != dlqReplayHeader {
			continue
		}
		key, err := parseDeadLetterKey(ctx, string(header.Value))
		if err != nil {
			return noDeadLetter, false, err
		}
		return key, true, nil
	}
	return noDeadLetter, false, nil
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

// deadLetterRecord upserts one failed record into the dead-letter table
// inside tx. A first failure inserts the row; a replay that fails again
// counts the attempt on the original row and refreshes its failure text.
//
// It runs after the failing statement's savepoint is rolled back, so the
// transaction is live and the dead-letter row commits with the batch's
// offset advance: the record is accounted for exactly once, in the table or
// in the ledger, never in neither.
func deadLetterRecord(ctx context.Context, tx pgx.Tx, rec *kgo.Record, cause error) error {
	key := DeadLetterKey{Topic: rec.Topic, Partition: rec.Partition, Offset: rec.Offset}
	origin, replaying, err := replayOrigin(ctx, rec)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.dlq_replay_header_rejected", slog.String("err", err.Error()))
		return fmt.Errorf("dead-letter %s: %w", key, err)
	}
	if replaying {
		key = origin
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit.events_dlq (received_at, topic, partition, "offset", payload, error, attempt_count, last_attempt_at)
		VALUES (now(), $1, $2, $3, $4, $5, 0, NULL)
		ON CONFLICT (topic, partition, "offset") DO UPDATE
		    SET attempt_count   = audit.events_dlq.attempt_count + 1,
		        last_attempt_at = now(),
		        error           = EXCLUDED.error
	`, key.Topic, key.Partition, key.Offset, rec.Value, cause.Error())
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
	origin, replaying, err := replayOrigin(ctx, rec)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.dlq_replay_header_rejected", slog.String("err", err.Error()))
		return fmt.Errorf("resolve dead-letter: %w", err)
	}
	if !replaying {
		return nil
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM audit.events_dlq WHERE topic = $1 AND partition = $2 AND "offset" = $3
	`, origin.Topic, origin.Partition, origin.Offset)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.dlq_resolve_failed",
			slog.String("err", err.Error()), slog.String("dead_letter", origin.String()))
		return fmt.Errorf("resolve dead-letter %s: %w", origin, err)
	}
	slog.InfoContext(ctx, "audit.consumer.dead_letter_resolved",
		slog.String("dead_letter", origin.String()),
		slog.Int64("rows", tag.RowsAffected()),
	)
	return nil
}
