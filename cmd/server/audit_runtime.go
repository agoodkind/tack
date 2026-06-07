package main

import (
	"context"
	"log/slog"

	mcptools "goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

type auditRuntime struct {
	Reader    *audit.Reader
	Redactor  *audit.Redactor
	Notarizer *audit.Notarizer
	Recorder  audit.Recorder
}

func setupAuditRuntime(ctx context.Context, cfg *config.Config) auditRuntime {
	auditRec := buildAuditRecorder(ctx, cfg)
	// Wrap once so internal automation can mute emission via audit.WithSuppressed.
	auditRec = audit.SuppressingRecorder{Inner: auditRec}
	mcptools.SetAuditRecorder(auditRec)
	auth.SetAuditRecorder(auditRec)

	notarizer := buildAuditNotarizer(ctx, cfg)
	if notarizer != nil {
		notarizer.Start(ctx)
	}
	return auditRuntime{
		Reader:    buildAuditReader(ctx, cfg),
		Redactor:  buildAuditRedactor(ctx, cfg),
		Notarizer: notarizer,
		Recorder:  auditRec,
	}
}

func (r auditRuntime) Close() {
	if r.Reader != nil {
		r.Reader.Close()
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

// buildAuditRecorder returns the recorder selected by config: a Yugabyte
// writer (optionally wrapped in a WAL recorder, then a Kafka dual recorder)
// when AUDIT_WRITER_DSN is set, else a NoopRecorder. Setup failures degrade to
// noop rather than blocking the server; production deploys must set the DSN.
func buildAuditRecorder(ctx context.Context, cfg *config.Config) audit.Recorder {
	if cfg.AuditWriterDSN == "" {
		slog.WarnContext(ctx, "audit.writer_disabled",
			slog.String("reason", "AUDIT_WRITER_DSN unset; ledger writes are noop"),
		)
		return audit.NoopRecorder{}
	}
	yb, err := audit.NewYBRecorder(ctx, cfg.AuditWriterDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.writer_setup_failed", slog.String("err", err.Error()))
		return audit.NoopRecorder{}
	}
	slog.InfoContext(ctx, "audit.writer_connected")
	walRec := buildAuditWALRecorder(ctx, cfg, yb)
	return wrapAuditWithKafka(ctx, cfg, walRec)
}

// buildAuditWALRecorder wraps the inner Yugabyte recorder in a WALRecorder when
// a WAL directory is configured. Falls through to the inner recorder on setup
// failure so audit recording is never gated on WAL availability.
func buildAuditWALRecorder(ctx context.Context, cfg *config.Config, inner audit.Recorder) audit.Recorder {
	if cfg.AuditWALDir == "" {
		return inner
	}
	wal, err := audit.NewWALRecorder(ctx, inner, audit.WALConfig{
		Dir:                cfg.AuditWALDir,
		MaxBytesPerSegment: 0,
		DrainInterval:      0,
		MaxLag:             0,
		MaxBacklogSegments: cfg.AuditWALMaxBacklogSegments,
		MaxBacklogAge:      cfg.AuditWALMaxBacklogAge,
		IdleRotateAfter:    cfg.AuditWALIdleRotateAfter,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.wal_setup_failed",
			slog.String("dir", cfg.AuditWALDir),
			slog.String("err", err.Error()),
		)
		return inner
	}
	stats := wal.Stats()
	telemetry.RegisterWALMetrics(telemetry.WALStatsSource{
		UnflushedSegments:      stats.UnflushedSegments,
		OldestUnflushedAgeSecs: stats.OldestUnflushedAgeSecs,
		LastDrainSuccessUnix:   stats.LastDrainSuccessUnix,
		IdleRotationsTotal:     stats.IdleRotationsTotal,
		WriteErrorsTotal:       stats.WriteErrorsTotal,
		DiskFreeBytes:          stats.DiskFreeBytes,
	})
	slog.InfoContext(ctx, "audit.wal_enabled", slog.String("dir", cfg.AuditWALDir))
	return wal
}

// wrapAuditWithKafka builds the dual recorder when Kafka brokers are
// configured, otherwise returns the WAL recorder unchanged.
func wrapAuditWithKafka(ctx context.Context, cfg *config.Config, walRec audit.Recorder) audit.Recorder {
	brokers := audit.SplitBrokers(cfg.AuditKafkaBrokers)
	if len(brokers) == 0 {
		return walRec
	}
	kafkaRec, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers:        brokers,
		Topic:          cfg.AuditKafkaTopic,
		ClientID:       cfg.AuditKafkaClientID,
		ProduceTimeout: cfg.AuditKafkaProduceTimeout,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.kafka_setup_failed", slog.String("err", err.Error()))
		return walRec
	}
	dual, err := audit.NewDualRecorder(kafkaRec, walRec)
	if err != nil {
		slog.ErrorContext(ctx, "audit.dual_setup_failed", slog.String("err", err.Error()))
		_ = kafkaRec.CloseContext(ctx)
		return walRec
	}
	slog.InfoContext(ctx, "audit.kafka_enabled",
		slog.Int("broker_count", len(brokers)),
		slog.String("topic", cfg.AuditKafkaTopic),
	)
	return dual
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
