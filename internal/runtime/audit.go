package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

const (
	// defaultAuditBrokerPingTimeout bounds each connection attempt when
	// configuration omits the produce timeout.
	defaultAuditBrokerPingTimeout = 15 * time.Second
	// defaultAuditBrokerReadyTimeout limits startup when configuration
	// omits the broker readiness timeout.
	defaultAuditBrokerReadyTimeout = 60 * time.Second
	// auditBrokerRetryInterval spaces connection attempts.
	auditBrokerRetryInterval = 2 * time.Second
)

type auditRuntime struct {
	Recorder audit.Recorder
}

// buildAuditRuntime configures the audit Recorder for one runtime graph.
// The graph's auth middleware and MCP handler use this recorder. Graph.Close
// does not change another graph's recorder (TACK-583). The server does not
// open ledger read or redaction connections. Only host operator commands
// read or redact the ledger.
func buildAuditRuntime(
	ctx context.Context,
	cfg *config.Config,
	spill audit.OutboxAppender,
) (auditRuntime, error) {
	auditRec, err := buildAuditRecorder(ctx, cfg)
	if err != nil {
		return auditRuntime{Recorder: nil}, err
	}

	// CanonicalRecorder assigns the event ID and time before SpillRecorder
	// stores the event. The relay must preserve both fields.
	// Only the Kafka producer uses SpillRecorder. The synchronous Yugabyte
	// recorder and relay write to the same database, which cannot provide
	// another store during an outage. The operator explicitly permits
	// NoopRecorder to discard events.
	var overflow audit.OutboxAppender
	if spill != nil {
		if _, isKafka := auditRec.(*audit.KafkaRecorder); isKafka {
			auditRec = &audit.SpillRecorder{Primary: auditRec, Spill: spill}
			overflow = spill
			slog.InfoContext(ctx, "audit.spill_enabled")
		}
	}

	// BufferedRecorder queues read-class events and flushes batches
	// asynchronously (TACK-506). CanonicalRecorder assigns each event's
	// ID and time before BufferedRecorder queues it.
	auditRec = audit.NewBufferedRecorder(
		ctx,
		auditRec,
		overflow,
		audit.BufferedConfig{
			Capacity:      cfg.AuditReadBufferCapacity,
			BatchSize:     cfg.AuditReadBatchSize,
			FlushInterval: cfg.AuditReadFlushInterval,
		},
	)
	wired := audit.Recorder(audit.CanonicalRecorder{Inner: auditRec})

	return auditRuntime{Recorder: wired}, nil
}

func (r auditRuntime) Close() {
	switch c := r.Recorder.(type) {
	case interface{ Close() error }:
		_ = c.Close()
	case interface{ Close() }:
		c.Close()
	}
}

// buildAuditRecorder selects the audit Recorder from configuration.
// AUDIT_KAFKA_BROKERS selects the Kafka producer; audit-consumer then writes
// audit.events. Otherwise, AUDIT_WRITER_DSN selects the synchronous Yugabyte
// recorder. Setup failures prevent the server from accepting requests.
// Only an explicit AUDIT_ALLOW_UNRECORDED setting permits discarded events.
func buildAuditRecorder(ctx context.Context, cfg *config.Config) (audit.Recorder, error) {
	brokers := audit.SplitBrokers(cfg.AuditKafkaBrokers)
	if len(brokers) > 0 {
		return buildKafkaRecorder(ctx, cfg, brokers)
	}

	if cfg.AuditWriterDSN == "" {
		return unconfiguredAuditRecorder(ctx, cfg)
	}

	yb, err := audit.NewYBRecorder(ctx, cfg.AuditWriterDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.writer_setup_failed", slog.String("err", err.Error()))

		return nil, fmt.Errorf(
			"runtime: audit writer unavailable, refusing to serve unrecorded: %w",
			err,
		)
	}

	slog.InfoContext(ctx, "audit.writer_connected")

	return yb, nil
}

// buildKafkaRecorder opens the producer and checks broker readiness.
// The franz-go client connects lazily. Constructing the client alone does
// not establish whether the broker can accept ledger writes.
func buildKafkaRecorder(
	ctx context.Context,
	cfg *config.Config,
	brokers []string,
) (audit.Recorder, error) {
	kafkaRec, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers:        brokers,
		Topic:          cfg.AuditKafkaTopic,
		ClientID:       cfg.AuditKafkaClientID,
		ProduceTimeout: cfg.AuditKafkaProduceTimeout,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.kafka_setup_failed", slog.String("err", err.Error()))

		return nil, fmt.Errorf(
			"runtime: audit producer unavailable, refusing to serve unrecorded: %w",
			err,
		)
	}

	if err := waitForBrokers(ctx, cfg, kafkaRec); err != nil {
		_ = kafkaRec.CloseContext(ctx)

		return nil, fmt.Errorf(
			"runtime: audit brokers unreachable, refusing to serve unrecorded: %w",
			err,
		)
	}

	slog.InfoContext(
		ctx,
		"audit.kafka_enabled",
		slog.Int("broker_count", len(brokers)),
		slog.String("topic", cfg.AuditKafkaTopic),
	)

	return kafkaRec, nil
}

// waitForBrokers retries readiness checks until the configured timeout.
// The broker can require more startup time than the app.
func waitForBrokers(ctx context.Context, cfg *config.Config, recorder *audit.KafkaRecorder) error {
	budget := cfg.AuditBrokerReadyTimeout
	if budget <= 0 {
		budget = defaultAuditBrokerReadyTimeout
	}

	attemptTimeout := cfg.AuditKafkaProduceTimeout
	if attemptTimeout <= 0 {
		attemptTimeout = defaultAuditBrokerPingTimeout
	}

	deadlineCtx, cancelDeadline := context.WithTimeout(ctx, budget)
	defer cancelDeadline()
	var lastErr error

	for {
		attemptCtx, cancelAttempt := context.WithTimeout(deadlineCtx, attemptTimeout)
		lastErr = recorder.Ready(attemptCtx)
		cancelAttempt()
		if lastErr == nil {
			return nil
		}

		select {
		case <-deadlineCtx.Done():
			slog.ErrorContext(
				ctx,
				"audit.brokers_unreachable",
				slog.String("budget", budget.String()),
				slog.String("err", lastErr.Error()),
			)

			return fmt.Errorf("after %s: %w", budget, lastErr)
		case <-time.After(auditBrokerRetryInterval):
		}
	}
}

// unconfiguredAuditRecorder rejects configuration without an audit backend
// unless AUDIT_ALLOW_UNRECORDED explicitly permits it. The permitted case
// logs an error before returning NoopRecorder.
func unconfiguredAuditRecorder(ctx context.Context, cfg *config.Config) (audit.Recorder, error) {
	if !cfg.AuditAllowUnrecorded {
		err := errors.New(
			"set AUDIT_KAFKA_BROKERS or AUDIT_WRITER_DSN, or set AUDIT_ALLOW_UNRECORDED to run without a ledger",
		)
		slog.ErrorContext(ctx, "audit.writer_unconfigured", slog.String("err", err.Error()))

		return nil, fmt.Errorf(
			"runtime: no audit backend configured, refusing to serve unrecorded: %w",
			err,
		)
	}

	slog.ErrorContext(
		ctx,
		"audit.unrecorded_acknowledged",
		slog.String("err", "AUDIT_ALLOW_UNRECORDED is set; this server records nothing"),
	)

	return audit.NoopRecorder{}, nil
}
