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

// dlqReplayHeader identifies the Kafka header that stores a replay's original
// dead-letter key as topic/partition/offset. The consumer resolves that row
// independently of the replay's new offset.
const dlqReplayHeader = "tack-dlq-replay"

// DeadLetterKey identifies one dead-letter row by the Kafka position of the
// record that produced it.
type DeadLetterKey struct {
	Topic     string
	Partition int32
	Offset    int64
}

// String renders the key in the replay header's format.
//
// The method separates the topic, decimal partition, and decimal offset with
// slashes. [parseDeadLetterKey] reads that format.
//
// 	value := key.String()
func (k DeadLetterKey) String() string {
	return k.Topic + "/" + strconv.FormatInt(int64(k.Partition), 10) + "/" + strconv.FormatInt(k.Offset, 10)
}

// noDeadLetter is the zero key returned when a header does not identify a row.
var noDeadLetter = DeadLetterKey{Topic: "", Partition: 0, Offset: 0}

// recordPayload returns the record value in the form the table stores. A
// tombstone or a producer without a value supplies a nil slice. The driver
// binds nil as SQL NULL, which the payload column's NOT NULL constraint
// rejects.
//
// Before empty-payload normalization, that dead-letter write failure rolled
// back the batch and stalled the partition indefinitely. The function converts
// nil to zero bytes for storage.
func recordPayload(rec *kgo.Record) []byte {
	if rec.Value == nil {
		return []byte{}
	}
	return rec.Value
}

// parseDeadLetterKey parses a replay header value. The function logs each parse
// failure once with the record's offset.
func parseDeadLetterKey(ctx context.Context, value string, offset int64) (DeadLetterKey, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 3 {
		err := fmt.Errorf("dead-letter key %q: want topic/partition/offset", value)
		slog.WarnContext(ctx, "audit.consumer.dlq_replay_header_ignored",
			slog.Int64("offset", offset), slog.String("err", err.Error()))
		return noDeadLetter, err
	}
	partition, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil {
		slog.WarnContext(ctx, "audit.consumer.dlq_replay_header_ignored",
			slog.Int64("offset", offset), slog.String("err", err.Error()))
		return noDeadLetter, fmt.Errorf("dead-letter key %q: partition: %w", value, err)
	}
	position, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		slog.WarnContext(ctx, "audit.consumer.dlq_replay_header_ignored",
			slog.Int64("offset", offset), slog.String("err", err.Error()))
		return noDeadLetter, fmt.Errorf("dead-letter key %q: offset: %w", value, err)
	}
	return DeadLetterKey{Topic: parts[0], Partition: int32(partition), Offset: position}, nil
}

// replayOrigin returns the original dead-letter key from a well-formed replay
// header. The parser logs a malformed value once, and the consumer handles that
// record as a fresh record rather than stalling the partition on a hostile or
// broken header. The ledger's identity claim prevents duplicate insertion.
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

// verifiedReplayOrigin returns a failed replay's original row only when its
// stored payload equals the record bytes. A header identifying a different
// payload does not qualify as a replay. A producer cannot increment attempts or
// modify an unrelated dead-letter row by changing the header.
func verifiedReplayOrigin(ctx context.Context, tx pgx.Tx, rec *kgo.Record) (DeadLetterKey, bool, error) {
	origin, replaying := replayOrigin(ctx, rec)
	if !replaying {
		return noDeadLetter, false, nil
	}
	var matches bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM audit.events_dlq
			 WHERE topic = $1 AND partition = $2 AND "offset" = $3 AND payload = $4
		)
	`, origin.Topic, origin.Partition, origin.Offset, recordPayload(rec)).Scan(&matches)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.dlq_origin_check_failed",
			slog.String("err", err.Error()), slog.String("dead_letter", origin.String()))
		return noDeadLetter, false, fmt.Errorf("check dead-letter origin %s: %w", origin, err)
	}
	if !matches {
		slog.WarnContext(ctx, "audit.consumer.dlq_replay_origin_mismatch",
			slog.String("dead_letter", origin.String()), slog.Int64("offset", rec.Offset))
		return noDeadLetter, false, nil
	}
	return origin, true, nil
}
