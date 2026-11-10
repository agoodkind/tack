// Package auditconsumer starts and stops the audit consumer service.
package auditconsumer

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

const (
	consumerDrainTimeout = 30 * time.Second
)

// Run loads the consumer configuration and runs the audit service.
//
// It starts the relay, consumer and metrics endpoint. It waits for an
// interrupt or termination signal, then drains and closes the service.
//
// Startup follows this order:
//  1. Load configuration and start telemetry.
//  2. Open the relay and consumer.
//  3. Start event processing and wait for a shutdown signal.
func Run() error {
	cfg, err := config.LoadAuditConsumerSettings()
	if err != nil {
		wrapped := fmt.Errorf("load audit consumer configuration: %w", err)
		slog.Error("audit_consumer.config_failed", slog.String("err", wrapped.Error()))

		return wrapped
	}

	return runConsumer(cfg)
}

func runConsumer(cfg config.AuditConsumerSettings) error {
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

	consumer, err := newAuditConsumer(ctx, cfg, audit.NewRecorderOutbox(relayRecorder))
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
	slog.Info("audit_consumer.draining")
	drainCtx, drainCancel := context.WithTimeout(context.Background(), consumerDrainTimeout)
	defer drainCancel()
	stopMetricsServer(metricsServer)

	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error(
					"audit_consumer.close_panic",
					slog.Any("panic", r),
					slog.String("err", fmt.Sprintf("%v", r)),
				)
			}

			close(done)
		}()
		if err := relay.Close(); err != nil {
			slog.Error("audit_consumer.relay_close_failed", slog.String("err", err.Error()))
		}

		if err := consumer.Close(); err != nil {
			slog.Error("audit_consumer.consumer_close_failed", slog.String("err", err.Error()))
		}
	}()
	select {
	case <-done:
		slog.Info("audit_consumer.stopped")
	case <-drainCtx.Done():
		slog.Error("audit_consumer.drain_timeout", slog.String("err", drainCtx.Err().Error()))
	}

	return nil
}
