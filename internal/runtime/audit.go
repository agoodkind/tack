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
	Recorder audit.Recorder
}

// buildAuditRuntime selects and wires the audit Recorder and installs it as
// the process-global sink for the MCP and auth boundaries. The server holds
// no ledger read or redaction connection: every audit read and redaction is
// an operator command on the host, never an MCP tool.
func buildAuditRuntime(ctx context.Context, cfg *config.Config) auditRuntime {
	auditRec := buildAuditRecorder(ctx, cfg)
	auditRec = audit.CanonicalRecorder{Inner: auditRec}
	mcptools.SetAuditRecorder(auditRec)
	auth.SetAuditRecorder(auditRec)
	return auditRuntime{Recorder: auditRec}
}

func (r auditRuntime) Close() {
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
