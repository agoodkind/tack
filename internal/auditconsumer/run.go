// Package auditconsumer runs the Kafka audit consumer and outbox relay.
package auditconsumer

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// Run starts the consumer and relay and drains them after a termination signal.
func Run(cfg config.AuditConsumerSettings) error {
	closer, err := telemetry.Setup(telemetry.LogConfig{
		Level:         cfg.LogLevel,
		JSONFile:      cfg.LogJSONFile,
		TextFile:      cfg.LogTextFile,
		DisableStdout: cfg.LogDisableStdout,
		MaxSizeMB:     cfg.LogMaxSizeMB,
		MaxBackups:    cfg.LogMaxBackups,
		MaxAgeDays:    cfg.LogMaxAgeDays,
		OTELEndpoint:  cfg.OTELEndpoint,
		Env:           cfg.Env,
	})
	if err != nil {
		return fmt.Errorf("telemetry setup: %w", err)
	}

	defer func() {
		if closer != nil {
			_ = closer.Close()
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	metricsServer := startMetricsServer(ctx, cfg.MetricsAddr)

	opened, err := newAuditRelay(ctx, cfg)
	if err != nil {
		return err
	}
	defer opened.Pool.Close()
	relay, relayRecorder := opened.Relay, opened.Recorder

	slog.InfoContext(
		ctx,
		"audit_consumer.starting",
		slog.String("topic", cfg.Topic),
		slog.String("group", cfg.GroupID),
	)

	consumer, err := newAuditConsumer(ctx, cfg)
	if err != nil {
		if closeErr := relayRecorder.CloseContext(ctx); closeErr != nil {
			slog.ErrorContext(
				ctx,
				"audit_consumer.relay_recorder_close_failed",
				slog.String("err", closeErr.Error()),
			)
		}

		return err
	}

	consumer.Start(ctx)
	relay.Start(ctx)
	slog.InfoContext(ctx, "audit_consumer.started")

	<-ctx.Done()
	drainConsumer(metricsServer, relay, consumer)

	return nil
}
