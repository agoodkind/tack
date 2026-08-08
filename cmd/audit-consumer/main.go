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

	"github.com/caarlos0/env/v11"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

type consumerEnv struct {
	Brokers         string        `env:"AUDIT_CONSUMER_KAFKA_BROKERS,required"`
	Topic           string        `env:"AUDIT_CONSUMER_KAFKA_TOPIC"      envDefault:"audit.events.v1"`
	GroupID         string        `env:"AUDIT_CONSUMER_GROUP_ID"         envDefault:"tack-audit-projector"`
	BatchSize       int           `env:"AUDIT_CONSUMER_BATCH_SIZE"       envDefault:"256"`
	PollInterval    time.Duration `env:"AUDIT_CONSUMER_POLL_INTERVAL"    envDefault:"250ms"`
	YugabyteDSN     string        `env:"AUDIT_CONSUMER_YUGABYTE_DSN,required"`
	ClickHouseDSN   string        `env:"AUDIT_CONSUMER_CLICKHOUSE_DSN"`
	SigningKeyPath  string        `env:"AUDIT_CONSUMER_SIGNING_KEY_PATH"`
	NotarizerPeriod time.Duration `env:"AUDIT_CONSUMER_NOTARIZER_PERIOD" envDefault:"60s"`
	ReconcilePeriod time.Duration `env:"AUDIT_CONSUMER_RECONCILE_PERIOD" envDefault:"30m"`
	ReconcileWindow time.Duration `env:"AUDIT_CONSUMER_RECONCILE_WINDOW" envDefault:"24h"`
	LagWarnMessages int64         `env:"TACK_AUDIT_CONSUMER_LAG_WARN_MESSAGES" envDefault:"1000"`
	SummaryEvery    int           `env:"TACK_AUDIT_CONSUMER_SUMMARY_EVERY" envDefault:"100"`
	PartitionPeriod time.Duration `env:"AUDIT_CONSUMER_PARTITION_PERIOD" envDefault:"24h"`
	MetricsAddr     string        `env:"AUDIT_CONSUMER_METRICS_ADDR" envDefault:"127.0.0.1:9109"`
	// FDBClusterFile enables the FoundationDB relay when set.
	FDBClusterFile string `env:"FDB_CLUSTER_FILE"`

	LogLevel         string `env:"LOG_LEVEL"`
	LogJSONFile      string `env:"LOG_JSON_FILE"`
	LogTextFile      string `env:"LOG_TEXT_FILE"`
	LogDisableStdout bool   `env:"LOG_DISABLE_STDOUT"`
	LogMaxSizeMB     int    `env:"LOG_MAX_SIZE_MB"  envDefault:"100"`
	LogMaxBackups    int    `env:"LOG_MAX_BACKUPS"  envDefault:"0"`
	LogMaxAgeDays    int    `env:"LOG_MAX_AGE_DAYS" envDefault:"0"`
	OTELEndpoint     string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
}

type foundationDBRelayOutbox struct {
	store *foundationdb.OpsOutboxStore
}

func (s foundationDBRelayOutbox) ReadOutboxFrom(
	ctx context.Context,
	mark []byte,
	limit int,
) ([]audit.RelayOutboxEntry, error) {
	entries, err := s.store.ReadOutboxFrom(ctx, mark, limit)
	if err != nil {
		slog.ErrorContext(ctx, "audit_consumer.relay_fdb_read_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read FoundationDB relay outbox: %w", err)
	}
	out := make([]audit.RelayOutboxEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, audit.RelayOutboxEntry{
			Mark:  entry.Mark,
			Event: entry.Event,
		})
	}
	return out, nil
}

func (s foundationDBRelayOutbox) ClearThrough(ctx context.Context, mark []byte) error {
	if err := s.store.ClearThrough(ctx, mark); err != nil {
		slog.ErrorContext(ctx, "audit_consumer.relay_fdb_clear_failed", slog.String("err", err.Error()))
		return fmt.Errorf("clear FoundationDB relay outbox: %w", err)
	}
	return nil
}

func newAuditRelay(
	ctx context.Context,
	cfg consumerEnv,
) (*audit.Relay, *pgxpool.Pool, *audit.KafkaRecorder, error) {
	relayPool, err := pgxpool.New(ctx, cfg.YugabyteDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit_consumer.relay_yugabyte_pool_failed", slog.String("err", err.Error()))
		return nil, nil, nil, fmt.Errorf("new relay Yugabyte pool: %w", err)
	}

	var relayFoundationDB audit.FoundationDBOutbox
	if cfg.FDBClusterFile != "" {
		stores, storesErr := foundationdb.NewStores(cfg.FDBClusterFile, relayPool)
		if storesErr != nil {
			relayPool.Close()
			slog.ErrorContext(ctx, "audit_consumer.relay_foundationdb_open_failed", slog.String("err", storesErr.Error()))
			return nil, nil, nil, fmt.Errorf("open relay FoundationDB stores: %w", storesErr)
		}
		relayFoundationDB = foundationDBRelayOutbox{store: stores.OpsOutbox}
	}

	relayRecorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers:        splitBrokers(cfg.Brokers),
		Topic:          cfg.Topic,
		ClientID:       "tack-audit-relay",
		ProduceTimeout: 0,
	})
	if err != nil {
		relayPool.Close()
		slog.ErrorContext(ctx, "audit_consumer.relay_recorder_failed", slog.String("err", err.Error()))
		return nil, nil, nil, fmt.Errorf("new relay recorder: %w", err)
	}

	relay, err := audit.NewRelay(audit.RelayConfig{
		Recorder:     relayRecorder,
		Yugabyte:     audit.NewPoolOutbox(relayPool),
		FoundationDB: relayFoundationDB,
		PollInterval: cfg.PollInterval,
		BatchSize:    cfg.BatchSize,
	})
	if err != nil {
		relayPool.Close()
		if closeErr := relayRecorder.CloseContext(ctx); closeErr != nil {
			slog.ErrorContext(ctx, "audit_consumer.relay_recorder_close_failed", slog.String("err", closeErr.Error()))
		}
		slog.ErrorContext(ctx, "audit_consumer.relay_failed", slog.String("err", err.Error()))
		return nil, nil, nil, fmt.Errorf("new audit relay: %w", err)
	}
	return relay, relayPool, relayRecorder, nil
}

func newAuditConsumer(ctx context.Context, cfg consumerEnv) (*audit.Consumer, error) {
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
		ReconcilePeriod: cfg.ReconcilePeriod,
		ReconcileWindow: cfg.ReconcileWindow,
		LagWarnMessages: cfg.LagWarnMessages,
		SummaryEvery:    cfg.SummaryEvery,
		PartitionPeriod: cfg.PartitionPeriod,
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
	var cfg consumerEnv
	if err := env.Parse(&cfg); err != nil {
		return fmt.Errorf("parse env: %w", err)
	}

	closer, err := telemetry.Setup(telemetry.LogConfig{
		Level:         cfg.LogLevel,
		JSONFile:      cfg.LogJSONFile,
		TextFile:      cfg.LogTextFile,
		DisableStdout: cfg.LogDisableStdout,
		MaxSizeMB:     cfg.LogMaxSizeMB,
		MaxBackups:    cfg.LogMaxBackups,
		MaxAgeDays:    cfg.LogMaxAgeDays,
		OTELEndpoint:  cfg.OTELEndpoint,
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

	relay, relayPool, relayRecorder, err := newAuditRelay(ctx, cfg)
	if err != nil {
		return err
	}
	defer relayPool.Close()

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
