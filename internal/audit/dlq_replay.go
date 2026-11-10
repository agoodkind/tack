package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"
	"goodkind.io/tack/internal/telemetry"
)

// ListDeadLetters reads up to limit dead-letter rows and payloads for replay.
//
// The method orders rows by attempt count, then age. Rows without attempts sort
// first. This order prevents repeatedly rejected payloads from excluding other
// rows on every run.
//
//	letters, err := reader.ListDeadLetters(ctx, limit)
func (r *Reader) ListDeadLetters(ctx context.Context, limit int) ([]DeadLetter, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("audit reader not configured")
	}

	rows, err := r.pool.Rows(
		r.pool.unsanitized.Query(
			ctx,
			`
		SELECT topic, partition, "offset", received_at, payload, error, attempt_count, last_attempt_at
		  FROM audit.events_dlq
		 ORDER BY attempt_count ASC, received_at ASC, topic ASC, partition ASC, "offset" ASC
		 LIMIT $1
	`,
			limit,
		),
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.dlq.list_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("list dead letters: %w", err)
	}
	defer rows.Close()
	out := make([]DeadLetter, 0)

	for rows.Next() {
		var row DeadLetter
		if err := rows.Scan(
			&row.Key.Topic,
			&row.Key.Partition,
			&row.Key.Offset,
			&row.ReceivedAt,
			&row.Payload,
			&row.Error,
			&row.AttemptCount,
			&row.LastAttemptAt,
		); err != nil {
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

// SummarizeDeadLetters groups dead-letter rows by failure text.
//
// Each result includes the row count and oldest and newest receipt times. The
// method orders results by descending count, then failure text.
//
//	summaries, err := reader.SummarizeDeadLetters(ctx)
func (r *Reader) SummarizeDeadLetters(ctx context.Context) ([]DeadLetterSummary, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("audit reader not configured")
	}

	rows, err := r.pool.Rows(
		r.pool.unsanitized.Query(
			ctx,
			`
		SELECT error, count(*), min(received_at), max(received_at)
		  FROM audit.events_dlq
		 GROUP BY error
		 ORDER BY count(*) DESC, error ASC
	`,
		),
	)
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

// Replay republishes a dead-letter row's exact payload bytes to the audit
// topic.
//
// The method includes the original row key in a header and deliberately leaves
// payload validation to the consumer. The consumer decodes and projects the
// record, then deletes a resolved row or counts another failed attempt. Local
// validation would hide attempts for payloads the decoder still rejects.
//
// The consumer remains the ledger's only writer. A decodable payload uses the
// producer's partition key to select the partition and consumer for its chain.
//
//	err := recorder.Replay(ctx, letter)
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
		slog.ErrorContext(
			ctx,
			"audit.dlq.replay_produce_failed",
			slog.String("dead_letter", letter.Key.String()),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("replay dead letter %s: %w", letter.Key, err)
	}

	telemetry.IncAuditKafkaProduce("ok")
	slog.InfoContext(
		ctx,
		"audit.dlq.replayed",
		slog.String("dead_letter", letter.Key.String()),
		slog.Int("attempt_count", letter.AttemptCount),
		slog.Time("received_at", letter.ReceivedAt),
	)

	return nil
}

// replayPartitionKey derives the producer's partition key from a dead-letter
// payload. The function returns nil when JSON decoding fails. Kafka may select
// any partition for that malformed payload; the consumer rejects it again and
// counts the attempt.
func replayPartitionKey(payload []byte) []byte {
	var ev Event
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil
	}

	return kafkaPartitionKey(ev)
}
