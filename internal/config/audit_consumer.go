package config

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/caarlos0/env/v11"
)

// AuditConsumerSettings configures the standalone audit consumer.
type AuditConsumerSettings struct {
	Brokers               string        `env:"AUDIT_CONSUMER_KAFKA_BROKERS,required"`
	Topic                 string        `env:"AUDIT_CONSUMER_KAFKA_TOPIC"      envDefault:"audit.events.v1"`
	GroupID               string        `env:"AUDIT_CONSUMER_GROUP_ID"         envDefault:"tack-audit-projector"`
	BatchSize             int           `env:"AUDIT_CONSUMER_BATCH_SIZE"       envDefault:"256"`
	PollInterval          time.Duration `env:"AUDIT_CONSUMER_POLL_INTERVAL"    envDefault:"250ms"`
	YugabyteDSN           string        `env:"AUDIT_CONSUMER_YUGABYTE_DSN,required"`
	ClickHouseDSN         string        `env:"AUDIT_CONSUMER_CLICKHOUSE_DSN"`
	SigningKeyPath        string        `env:"AUDIT_CONSUMER_SIGNING_KEY_PATH"`
	NotarizerPeriod       time.Duration `env:"AUDIT_CONSUMER_NOTARIZER_PERIOD" envDefault:"60s"`
	SigningHost           string        `env:"AUDIT_CONSUMER_SIGNING_HOST"`
	ReconcilePeriod       time.Duration `env:"AUDIT_CONSUMER_RECONCILE_PERIOD" envDefault:"30m"`
	ReconcileWindow       time.Duration `env:"AUDIT_CONSUMER_RECONCILE_WINDOW" envDefault:"24h"`
	LagWarnMessages       int64         `env:"TACK_AUDIT_CONSUMER_LAG_WARN_MESSAGES" envDefault:"1000"`
	SummaryEvery          int           `env:"TACK_AUDIT_CONSUMER_SUMMARY_EVERY" envDefault:"100"`
	PartitionPeriod       time.Duration `env:"AUDIT_CONSUMER_PARTITION_PERIOD" envDefault:"24h"`
	TopicRetention        time.Duration `env:"AUDIT_CONSUMER_TOPIC_RETENTION"  envDefault:"8760h"`
	MetricsAddr           string        `env:"AUDIT_CONSUMER_METRICS_ADDR" envDefault:"127.0.0.1:9109"`
	FDBClusterFile        string        `env:"FDB_CLUSTER_FILE"`
	FDBTransactionTimeout time.Duration `env:"FDB_TRANSACTION_TIMEOUT" envDefault:"5s"`

	Env string `env:"ENV,required"`

	LogLevel         string `env:"LOG_LEVEL"`
	LogJSONFile      string `env:"LOG_JSON_FILE"`
	LogTextFile      string `env:"LOG_TEXT_FILE"`
	LogDisableStdout bool   `env:"LOG_DISABLE_STDOUT"`
	LogMaxSizeMB     int    `env:"LOG_MAX_SIZE_MB"  envDefault:"100"`
	LogMaxBackups    int    `env:"LOG_MAX_BACKUPS"  envDefault:"0"`
	LogMaxAgeDays    int    `env:"LOG_MAX_AGE_DAYS" envDefault:"0"`
	OTELEndpoint     string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
}

// LoadAuditConsumerSettings resolves consumer configuration and secret references.
func LoadAuditConsumerSettings() (AuditConsumerSettings, error) {
	environment, err := secretEnvironment(serviceSecretNames)
	if err != nil {
		return AuditConsumerSettings{}, err
	}
	var settings AuditConsumerSettings
	if err := env.ParseWithOptions(&settings, env.Options{Environment: environment}); err != nil {
		wrapped := fmt.Errorf("parse audit consumer environment: %w", err)
		slog.Error("config.consumer_failed", slog.String("err", wrapped.Error()))
		return settings, wrapped
	}
	return settings, nil
}
