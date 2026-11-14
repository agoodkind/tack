package audit

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/twmb/franz-go/pkg/kgo"
)

// dlqReplayHeader identifies the header containing the original dead-letter
// topic/partition/offset key, independent of the replay record's Kafka offset.
const dlqReplayHeader = "tack-dlq-replay"

// DeadLetterKey identifies a dead-letter row by its original Kafka position.
type DeadLetterKey struct {
	Topic     string
	Partition int32
	Offset    int64
}

// String encodes the key for the replay header.
func (k DeadLetterKey) String() string {
	return k.Topic + "/" + strconv.FormatInt(int64(k.Partition), 10) + "/" + strconv.FormatInt(
		k.Offset,
		10,
	)
}

// noDeadLetter is the zero key when a record does not identify a dead-letter row.
var noDeadLetter = DeadLetterKey{Topic: "", Partition: 0, Offset: 0}

// recordPayload normalizes nil tombstone values to empty bytes. SQL NULL would
// violate the dead-letter payload constraint and repeatedly fail the batch.
func recordPayload(rec *kgo.Record) []byte {
	if rec.Value == nil {
		return []byte{}
	}

	return rec.Value
}

// parseDeadLetterKey decodes a replay header and logs invalid values once
// with the replay record's offset.
func parseDeadLetterKey(ctx context.Context, value string, offset int64) (DeadLetterKey, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 3 {
		err := fmt.Errorf("dead-letter key %q: want topic/partition/offset", value)
		slog.WarnContext(
			ctx,
			"audit.consumer.dlq_replay_header_ignored",
			slog.Int64("offset", offset),
			slog.String("err", err.Error()),
		)

		return noDeadLetter, err
	}

	partition, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil {
		slog.WarnContext(
			ctx,
			"audit.consumer.dlq_replay_header_ignored",
			slog.Int64("offset", offset),
			slog.String("err", err.Error()),
		)

		return noDeadLetter, fmt.Errorf("dead-letter key %q: partition: %w", value, err)
	}

	position, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		slog.WarnContext(
			ctx,
			"audit.consumer.dlq_replay_header_ignored",
			slog.Int64("offset", offset),
			slog.String("err", err.Error()),
		)

		return noDeadLetter, fmt.Errorf("dead-letter key %q: offset: %w", value, err)
	}

	return DeadLetterKey{Topic: parts[0], Partition: int32(partition), Offset: position}, nil
}

// replayOrigin returns the original key from a valid replay header. Invalid
// headers log once and do not prevent fresh-record processing. The ledger's
// identity claim still rejects duplicate event IDs.
func replayOrigin(ctx context.Context, rec *kgo.Record) (DeadLetterKey, bool) {
	for _, header := range rec.Headers {
		if header.Key != dlqReplayHeader {
			continue
		}

		key, err := parseDeadLetterKey(ctx, string(header.Value), rec.Offset)
		if err != nil {
			return noDeadLetter, false
		}

		return key, true
	}

	return noDeadLetter, false
}

// verifiedReplayOrigin requires the referenced row's payload to equal the
// replay record bytes before updating attempts. A header alone must not
// authorize changes to an unrelated dead letter.
func verifiedReplayOrigin(
	ctx context.Context,
	tx pgx.Tx,
	rec *kgo.Record,
) (DeadLetterKey, bool, error) {
	origin, replaying := replayOrigin(ctx, rec)
	if !replaying {
		return noDeadLetter, false, nil
	}

	var matches bool
	err := tx.QueryRow(
		ctx,
		`
		SELECT EXISTS (
			SELECT 1 FROM audit.events_dlq
			 WHERE topic = $1 AND partition = $2 AND "offset" = $3 AND payload = $4
		)
	`,
		origin.Topic,
		origin.Partition,
		origin.Offset,
		recordPayload(rec),
	).Scan(
		&matches,
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.consumer.dlq_origin_check_failed",
			slog.String("err", err.Error()),
			slog.String("dead_letter", origin.String()),
		)

		return noDeadLetter, false, fmt.Errorf("check dead-letter origin %s: %w", origin, err)
	}

	if !matches {
		slog.WarnContext(
			ctx,
			"audit.consumer.dlq_replay_origin_mismatch",
			slog.String("dead_letter", origin.String()),
			slog.Int64("offset", rec.Offset),
		)

		return noDeadLetter, false, nil
	}

	return origin, true, nil
}
