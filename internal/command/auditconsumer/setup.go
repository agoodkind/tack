package auditconsumer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
)

const consumerService = "tack-audit-consumer"

func newAuditRelay(
	ctx context.Context,
	cfg config.AuditConsumerSettings,
) (auditrelay.Opened, error) {
	opened, err := auditrelay.Open(
		ctx,
		auditrelay.Config{
			Brokers:               splitBrokers(cfg.Brokers),
			Topic:                 cfg.Topic,
			YugabyteDSN:           cfg.YugabyteDSN,
			FDBClusterFile:        cfg.FDBClusterFile,
			FDBTransactionTimeout: cfg.FDBTransactionTimeout,
			PollInterval:          cfg.PollInterval,
			BatchSize:             cfg.BatchSize,
			LeaseDuration:         0,
			AfterProduce:          nil,
			AfterLeaseAcquired:    nil,
		},
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit_consumer.relay_open_failed", slog.String("err", err.Error()))

		return opened, fmt.Errorf("open the audit relay: %w", err)
	}

	return opened, nil
}

func newAuditConsumer(
	ctx context.Context,
	cfg config.AuditConsumerSettings,
	maintenance audit.OutboxWriter,
) (*audit.Consumer, error) {
	signingHost := cfg.SigningHost
	if signingHost == "" {
		hostname, err := os.Hostname()
		if err != nil {
			slog.ErrorContext(
				ctx,
				"audit_consumer.hostname_failed",
				slog.String("err", err.Error()),
			)

			return nil, fmt.Errorf("read the hostname for the signing host: %w", err)
		}

		signingHost = hostname
	}

	consumer, err := audit.NewConsumer(
		ctx,
		audit.ConsumerConfig{
			Brokers:         splitBrokers(cfg.Brokers),
			Topic:           cfg.Topic,
			GroupID:         cfg.GroupID,
			BatchSize:       cfg.BatchSize,
			PollInterval:    cfg.PollInterval,
			YugabyteDSN:     cfg.YugabyteDSN,
			SigningKeyPath:  cfg.SigningKeyPath,
			NotarizerPeriod: cfg.NotarizerPeriod,
			SigningHost:     signingHost,
			LagWarnMessages: cfg.LagWarnMessages,
			SummaryEvery:    cfg.SummaryEvery,
			PartitionPeriod: cfg.PartitionPeriod,
			TopicRetention:  cfg.TopicRetention,
		},
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit_consumer.consumer_failed", slog.String("err", err.Error()))

		return nil, fmt.Errorf("new consumer: %w", err)
	}

	consumer.RecordPartitionRuns(
		maintenance,
		audit.ServiceActor(cli.ServiceActorID(consumerService), consumerService),
	)

	return consumer, nil
}

func splitBrokers(raw string) []string {
	parts := strings.Split(raw, ",")

	out := parts[:0]
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s == "" {
			continue
		}

		out = append(out, s)
	}

	return out
}
