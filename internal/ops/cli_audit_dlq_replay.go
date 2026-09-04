package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

func runAuditDLQReplay(ctx context.Context, f *cli.Factory, input auditDLQReplayInput, sink clispec.ResultSink, execute bool) error {
	if input.Limit <= 0 {
		return errors.New("--limit must be at least 1")
	}
	reader, err := auditDLQReader(ctx, f)
	if err != nil {
		return err
	}
	defer reader.Close()
	letters, err := reader.ListDeadLetters(ctx, input.Limit)
	if err != nil {
		slog.ErrorContext(ctx, "audit.dlq.list_for_replay_failed", slog.String("err", err.Error()))
		return fmt.Errorf("list the dead-letter rows to replay: %w", err)
	}
	result := auditDLQReplayResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.audit.dlq.replay", DryRun: !execute,
		Limit: input.Limit, Selected: len(letters), Replayed: 0,
	}
	if !execute || len(letters) == 0 {
		return writeAuditDLQResult(ctx, sink, result)
	}
	producer, err := auditDLQProducer(ctx, f)
	if err != nil {
		return err
	}
	defer func() { _ = producer.CloseContext(ctx) }()
	for _, letter := range letters {
		if err := producer.Replay(ctx, letter); err != nil {
			slog.ErrorContext(ctx, "audit.dlq.replay_stopped",
				slog.Int("replayed", result.Replayed), slog.Int("selected", len(letters)),
				slog.String("err", err.Error()))
			return fmt.Errorf("replayed %d of %d dead letters: %w", result.Replayed, len(letters), err)
		}
		result.Replayed++
	}
	return writeAuditDLQResult(ctx, sink, result)
}

// auditDLQProducer opens the same producer the app records through, so a
// replay reaches the topic the consumer reads.
func auditDLQProducer(ctx context.Context, f *cli.Factory) (*audit.KafkaRecorder, error) {
	brokers := strings.TrimSpace(f.Cfg.AuditKafkaBrokers)
	if brokers == "" {
		err := errors.New("the dead-letter replay needs AUDIT_KAFKA_BROKERS")
		slog.ErrorContext(ctx, "audit.dlq.brokers_missing", slog.String("err", err.Error()))
		return nil, err
	}
	producer, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers:        audit.SplitBrokers(brokers),
		Topic:          f.Cfg.AuditKafkaTopic,
		ClientID:       "tack-audit-dlq-replay",
		ProduceTimeout: f.Cfg.AuditKafkaProduceTimeout,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.dlq.producer_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("open the audit producer for the replay: %w", err)
	}
	return producer, nil
}
