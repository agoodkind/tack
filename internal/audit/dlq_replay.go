package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
	"goodkind.io/tack/internal/telemetry"
)

// ListDeadLetters reads up to limit dead-letter rows with their payloads for
// a replay: rows never attempted first, then the fewest attempts, oldest
// first within a count, so a payload that can never land does not sit at
// the front of every run and starve the rows behind it.
func (r *Reader) ListDeadLetters(ctx context.Context, limit int) ([]DeadLetter, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("audit reader not configured")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT topic, partition, "offset", received_at, payload, error, attempt_count, last_attempt_at
		  FROM audit.events_dlq
		 ORDER BY attempt_count ASC, received_at ASC, topic ASC, partition ASC, "offset" ASC
		 LIMIT $1
	`, limit)
	if err != nil {
		slog.ErrorContext(ctx, "audit.dlq.list_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("list dead letters: %w", err)
	}
	defer rows.Close()
	out := make([]DeadLetter, 0)
	for rows.Next() {
		var row DeadLetter
		if err := rows.Scan(&row.Key.Topic, &row.Key.Partition, &row.Key.Offset, &row.ReceivedAt,
			&row.Payload, &row.Error, &row.AttemptCount, &row.LastAttemptAt); err != nil {
			slog.ErrorContext(ctx, "audit.dlq.scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("scan dead letter: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.dlq.rows_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read dead letters: %w", err)
	}
	return out, nil
}

// SummarizeDeadLetters groups the dead-letter table by failure text.
func (r *Reader) SummarizeDeadLetters(ctx context.Context) ([]DeadLetterSummary, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("audit reader not configured")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT error, count(*), min(received_at), max(received_at)
		  FROM audit.events_dlq
		 GROUP BY error
		 ORDER BY count(*) DESC, error ASC
	`)
	if err != nil {
		slog.ErrorContext(ctx, "audit.dlq.summary_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("summarize dead letters: %w", err)
	}
	defer rows.Close()
	out := make([]DeadLetterSummary, 0)
	for rows.Next() {
		var row DeadLetterSummary
		if err := rows.Scan(&row.Error, &row.Count, &row.Oldest, &row.Newest); err != nil {
			slog.ErrorContext(ctx, "audit.dlq.summary_scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("scan dead-letter summary: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.dlq.summary_rows_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read dead-letter summary: %w", err)
	}
	return out, nil
}

// Replay re-publishes one dead-letter row's payload to the audit topic,
// byte for byte, under a header naming the row. The consumer decodes and
// projects it exactly as it would a fresh record and, as the ledger's one
// writer, deletes the row when the event lands or counts the attempt when it
// fails again. The payload is not validated here on purpose: a row the
// current decoder still rejects must reach the consumer to be counted as
// another failed attempt, and a replay that refused it first would hide
// that. The partition key is the one the producer would have used, read from
// the payload when it decodes, so the replay lands on the partition that
// owns the event's chain and no second consumer advances it.
func (k *KafkaRecorder) Replay(ctx context.Context, letter DeadLetter) error {
	produceCtx, cancel := context.WithTimeout(ctx, k.produceTimeout)
	defer cancel()
	rec := &kgo.Record{
		Topic:   k.topic,
		Key:     replayPartitionKey(letter.Payload),
		Value:   letter.Payload,
		Headers: []kgo.RecordHeader{{Key: dlqReplayHeader, Value: []byte(letter.Key.String())}},
	}
	telemetry.IncAuditKafkaProduceInflight()
	start := monoStart()
	res := k.client.ProduceSync(produceCtx, rec)
	telemetry.DecAuditKafkaProduceInflight()
	telemetry.ObserveAuditKafkaProduceLatency(sinceMs(start))
	if err := res.FirstErr(); err != nil {
		telemetry.IncAuditKafkaProduce("error")
		slog.ErrorContext(ctx, "audit.dlq.replay_produce_failed",
			slog.String("dead_letter", letter.Key.String()),
			slog.String("err", err.Error()))
		return fmt.Errorf("replay dead letter %s: %w", letter.Key, err)
	}
	telemetry.IncAuditKafkaProduce("ok")
	slog.InfoContext(ctx, "audit.dlq.replayed",
		slog.String("dead_letter", letter.Key.String()),
		slog.Int("attempt_count", letter.AttemptCount),
		slog.Time("received_at", letter.ReceivedAt))
	return nil
}

// replayPartitionKey derives the producer's partition key from a dead-letter
// payload. A payload that does not decode gets no key and lands anywhere,
// which is harmless: the consumer will refuse it again and count the attempt.
func replayPartitionKey(payload []byte) []byte {
	var ev Event
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil
	}
	return kafkaPartitionKey(ev)
}
