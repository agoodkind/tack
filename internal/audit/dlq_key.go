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

// noDeadLetter is the zero key, returned where no row is named.
var noDeadLetter = DeadLetterKey{Topic: "", Partition: 0, Offset: 0}

// parseDeadLetterKey reads a key back out of a replay header value. A value
// that does not parse is logged here, once, with the record's offset.
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

// replayOrigin returns the dead-letter row a record is replaying, when the
// record carries a well-formed replay header. A header that does not parse
// is reported once and then ignored, so a hostile or broken producer cannot
// wedge a partition with it: the record is handled as a fresh one, which the
// ledger's identity claim keeps from landing twice.
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

// verifiedReplayOrigin returns the dead-letter row a failed record is
// replaying only when that row's stored payload is the record's bytes. A
// header that names a row whose payload differs is treated as no replay, so
// a producer cannot count attempts against, or otherwise touch, a dead
// letter it did not carry.
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
	`, origin.Topic, origin.Partition, origin.Offset, rec.Value).Scan(&matches)
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
