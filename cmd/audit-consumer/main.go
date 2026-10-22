// Package main runs the audit consumer. It tails the Kafka topic the producer
// writes to, projects each event into Yugabyte audit.events and ClickHouse
// audit.events_olap as the only writer of the chain, and runs the embedded
// notarizer goroutine.
package main

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

func newAuditRelay(ctx context.Context, cfg config.AuditConsumerSettings) (auditrelay.Opened, error) {
	opened, err := auditrelay.Open(ctx, auditrelay.Config{
		Brokers: splitBrokers(cfg.Brokers), Topic: cfg.Topic, YugabyteDSN: cfg.YugabyteDSN,
		FDBClusterFile: cfg.FDBClusterFile, FDBTransactionTimeout: cfg.FDBTransactionTimeout,
		PollInterval: cfg.PollInterval, BatchSize: cfg.BatchSize,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit_consumer.relay_open_failed", slog.String("err", err.Error()))
		return opened, fmt.Errorf("open the audit relay: %w", err)
	}
	return opened, nil
}

func newAuditConsumer(ctx context.Context, cfg config.AuditConsumerSettings) (*audit.Consumer, error) {
	signingHost := cfg.SigningHost
	if signingHost == "" {
		hostname, err := os.Hostname()
		if err != nil {
			slog.ErrorContext(ctx, "audit_consumer.hostname_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("read the hostname for the signing host: %w", err)
		}
		signingHost = hostname
	}
	consumer, err := audit.NewConsumer(ctx, audit.ConsumerConfig{
		Brokers:         splitBrokers(cfg.Brokers),
		Topic:           cfg.Topic,
		GroupID:         cfg.GroupID,
		BatchSize:       cfg.BatchSize,
		PollInterval:    cfg.PollInterval,
		YugabyteDSN:     cfg.YugabyteDSN,
		ClickHouseDSN:   cfg.ClickHouseDSN,
		SigningKeyPath:  cfg.SigningKeyPath,
		NotarizerPeriod: cfg.NotarizerPeriod,
		SigningHost:     signingHost,
		ReconcilePeriod: cfg.ReconcilePeriod,
		ReconcileWindow: cfg.ReconcileWindow,
		LagWarnMessages: cfg.LagWarnMessages,
		SummaryEvery:    cfg.SummaryEvery,
		PartitionPeriod: cfg.PartitionPeriod,
		TopicRetention:  cfg.TopicRetention,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit_consumer.consumer_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("new consumer: %w", err)
	}
	return consumer, nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("audit_consumer.exit", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func run() error {
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

	slog.InfoContext(ctx, "audit_consumer.starting",
		slog.String("topic", cfg.Topic),
		slog.String("group", cfg.GroupID),
	)

	consumer, err := newAuditConsumer(ctx, cfg)
	if err != nil {
		if closeErr := relayRecorder.CloseContext(ctx); closeErr != nil {
			slog.ErrorContext(ctx, "audit_consumer.relay_recorder_close_failed", slog.String("err", closeErr.Error()))
		}
		return err
	}

	consumer.Start(ctx)
	relay.Start(ctx)
	slog.InfoContext(ctx, "audit_consumer.started")

	<-ctx.Done()
	slog.Info("audit_consumer.draining")
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer drainCancel()
	if metricsServer != nil {
		metricsCtx, metricsCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := metricsServer.Shutdown(metricsCtx); err != nil {
			slog.Error("server.metrics_shutdown_failed", slog.String("err", err.Error()))
		}
		metricsCancel()
	}
	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("audit_consumer.close_panic",
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
		slog.Warn("audit_consumer.drain_timeout")
	}
	return nil
}

func startMetricsServer(ctx context.Context, addr string) *http.Server {
	if addr == "" {
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle("/debug/vars", expvar.Handler())
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.InfoContext(ctx, "server.metrics_listening", slog.String("addr", addr))
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "server.metrics_panic",
					slog.Any("panic", recovered),
					slog.String("err", fmt.Sprintf("%v", recovered)),
				)
			}
		}()
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.ErrorContext(ctx, "server.metrics_failed", slog.String("err", err.Error()))
		}
	}()
	return server
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
