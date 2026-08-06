package runtime

import (
	"context"
	"log/slog"

	mcptools "goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/config"
)

type auditRuntime struct {
	Reader     *audit.Reader
	ClickHouse *audit.ClickHouseReader
	Querier    audit.RowQuerier
	Redactor   *audit.Redactor
	Notarizer  *audit.Notarizer
	Recorder   audit.Recorder
}

// buildAuditRuntime selects and wires the audit Recorder, installs it as the
// process-global sink for the MCP and auth boundaries, starts the notarizer when
// configured, and opens the reader/redactor/querier used by the audit MCP tools.
func buildAuditRuntime(ctx context.Context, cfg *config.Config) auditRuntime {
	auditRec := buildAuditRecorder(ctx, cfg)
	// Wrap once so internal automation can mute emission via audit.WithSuppressed.
	auditRec = audit.SuppressingRecorder{Inner: auditRec}
	mcptools.SetAuditRecorder(auditRec)
	auth.SetAuditRecorder(auditRec)

	notarizer := buildAuditNotarizer(ctx, cfg)
	if notarizer != nil {
		notarizer.Start(ctx)
	}

	reader := buildAuditReader(ctx, cfg)
	clickHouse := buildAuditClickHouseReader(ctx, cfg)
	var querier audit.RowQuerier
	if reader != nil {
		querier = audit.NewQueryRouter(reader, clickHouse, cfg.AuditQueryRecentWindow)
	}
	return auditRuntime{
		Reader:     reader,
		ClickHouse: clickHouse,
		Querier:    querier,
		Redactor:   buildAuditRedactor(ctx, cfg),
		Notarizer:  notarizer,
		Recorder:   auditRec,
	}
}

func (r auditRuntime) Close() {
	if r.Reader != nil {
		r.Reader.Close()
	}
	if r.ClickHouse != nil {
		r.ClickHouse.Close()
	}
	if r.Redactor != nil {
		r.Redactor.Close()
	}
	if r.Notarizer != nil {
		_ = r.Notarizer.Close()
	}
	switch c := r.Recorder.(type) {
	case interface{ Close() error }:
		_ = c.Close()
	case interface{ Close() }:
		c.Close()
	}
}

// buildAuditRecorder selects the audit Recorder by configuration. When
// AUDIT_KAFKA_BROKERS is set it returns the Kafka producer, so Record publishes
// to Kafka and the audit-consumer becomes the only writer of audit.events.
// Otherwise, when AUDIT_WRITER_DSN is set, it returns the synchronous Yugabyte
// recorder. With neither configured it returns a NoopRecorder. A setup failure
// degrades to noop rather than blocking the server: audit is layered defense,
// not the primary path. Production deploys MUST set one of the two.
func buildAuditRecorder(ctx context.Context, cfg *config.Config) audit.Recorder {
	brokers := audit.SplitBrokers(cfg.AuditKafkaBrokers)
	if len(brokers) > 0 {
		kafkaRec, err := audit.NewKafkaRecorder(audit.KafkaConfig{
			Brokers:        brokers,
			Topic:          cfg.AuditKafkaTopic,
			ClientID:       cfg.AuditKafkaClientID,
			ProduceTimeout: cfg.AuditKafkaProduceTimeout,
		})
		if err != nil {
			slog.ErrorContext(ctx, "audit.kafka_setup_failed", slog.String("err", err.Error()))
			return audit.NoopRecorder{}
		}
		slog.InfoContext(ctx, "audit.kafka_enabled",
			slog.Int("broker_count", len(brokers)),
			slog.String("topic", cfg.AuditKafkaTopic),
		)
		return kafkaRec
	}
	if cfg.AuditWriterDSN == "" {
		slog.WarnContext(ctx, "audit.writer_disabled",
			slog.String("reason", "AUDIT_KAFKA_BROKERS and AUDIT_WRITER_DSN unset; ledger writes are noop"),
		)
		return audit.NoopRecorder{}
	}
	yb, err := audit.NewYBRecorder(ctx, cfg.AuditWriterDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.writer_setup_failed", slog.String("err", err.Error()))
		return audit.NoopRecorder{}
	}
	slog.InfoContext(ctx, "audit.writer_connected")
	return yb
}

// buildAuditClickHouseReader opens the ClickHouse read connection when
// AUDIT_CLICKHOUSE_DSN is set. nil means tack_audit_query reads Yugabyte only.
func buildAuditClickHouseReader(ctx context.Context, cfg *config.Config) *audit.ClickHouseReader {
	if cfg.AuditClickHouseDSN == "" {
		slog.InfoContext(ctx, "audit.clickhouse_reader_disabled",
			slog.String("reason", "AUDIT_CLICKHOUSE_DSN unset; audit queries read Yugabyte only"),
		)
		return nil
	}
	rd, err := audit.NewClickHouseReader(ctx, cfg.AuditClickHouseDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.clickhouse_reader_setup_failed", slog.String("err", err.Error()))
		return nil
	}
	slog.InfoContext(ctx, "audit.clickhouse_reader_connected")
	return rd
}

// buildAuditReader opens the audit_reader pool when AUDIT_READER_DSN is set.
// nil return means the audit query MCP tools are not registered.
func buildAuditReader(ctx context.Context, cfg *config.Config) *audit.Reader {
	if cfg.AuditReaderDSN == "" {
		slog.InfoContext(ctx, "audit.reader_disabled",
			slog.String("reason", "AUDIT_READER_DSN unset; audit query tools disabled"),
		)
		return nil
	}
	rd, err := audit.NewReader(ctx, cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.reader_setup_failed", slog.String("err", err.Error()))
		return nil
	}
	slog.InfoContext(ctx, "audit.reader_connected")
	return rd
}

// buildAuditRedactor opens the audit_redactor pool when AUDIT_REDACTOR_DSN is
// set. nil disables the GDPR redaction MCP tool.
func buildAuditRedactor(ctx context.Context, cfg *config.Config) *audit.Redactor {
	if cfg.AuditRedactorDSN == "" {
		slog.InfoContext(ctx, "audit.redactor_disabled",
			slog.String("reason", "AUDIT_REDACTOR_DSN unset; redaction tool disabled"),
		)
		return nil
	}
	rd, err := audit.NewRedactor(ctx, cfg.AuditRedactorDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.redactor_setup_failed", slog.String("err", err.Error()))
		return nil
	}
	slog.InfoContext(ctx, "audit.redactor_connected")
	return rd
}

// buildAuditNotarizer assembles the periodic Merkle-root notarizer when a
// signing key path is configured. Reuses the audit_writer DSN.
func buildAuditNotarizer(ctx context.Context, cfg *config.Config) *audit.Notarizer {
	if cfg.AuditSigningKeyPath == "" || cfg.AuditWriterDSN == "" {
		slog.InfoContext(ctx, "audit.notarizer_disabled",
			slog.String("reason", "AUDIT_SIGNING_KEY_PATH or AUDIT_WRITER_DSN unset"),
		)
		return nil
	}
	n, err := audit.NewNotarizer(ctx, cfg.AuditWriterDSN, audit.NotarizerConfig{
		SigningKeyPath: cfg.AuditSigningKeyPath,
		Period:         0,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.notarizer_setup_failed", slog.String("err", err.Error()))
		return nil
	}
	slog.InfoContext(ctx, "audit.notarizer_started", slog.String("key_path", cfg.AuditSigningKeyPath))
	return n
}
