package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	mcptools "goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/config"
)

const (
	// defaultAuditBrokerPingTimeout bounds one reachability attempt when no
	// produce timeout is configured, so a dead broker delays the refusal
	// rather than hanging the process before it can report one.
	defaultAuditBrokerPingTimeout = 15 * time.Second
	// defaultAuditBrokerReadyTimeout is the whole startup budget when none is
	// configured.
	defaultAuditBrokerReadyTimeout = 60 * time.Second
	// auditBrokerRetryInterval spaces the attempts inside that budget.
	auditBrokerRetryInterval = 2 * time.Second
)

type auditRuntime struct {
	Recorder audit.Recorder
}

// buildAuditRuntime selects and wires the audit Recorder and installs it as
// the process-global sink for the MCP and auth boundaries. The server holds
// no ledger read or redaction connection: every audit read and redaction is
// an operator command on the host, never an MCP tool.
func buildAuditRuntime(ctx context.Context, cfg *config.Config) (auditRuntime, error) {
	auditRec, err := buildAuditRecorder(ctx, cfg)
	if err != nil {
		return auditRuntime{Recorder: nil}, err
	}
	wired := audit.Recorder(audit.CanonicalRecorder{Inner: auditRec})
	mcptools.SetAuditRecorder(wired)
	auth.SetAuditRecorder(wired)
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

// buildAuditRecorder selects the audit Recorder by configuration, and returns
// an error rather than a recorder that discards events. When
// AUDIT_KAFKA_BROKERS is set it returns the Kafka producer, so Record
// publishes to Kafka and the audit-consumer becomes the only writer of
// audit.events. Otherwise, when AUDIT_WRITER_DSN is set, it returns the
// synchronous Yugabyte recorder.
//
// Every failure refuses. A server that cannot record is a server whose
// actions leave no evidence, and a caller cannot tell the difference from the
// outside, so the process must not reach the point of serving traffic. The one
// path that yields a discarding recorder is the operator declaring through
// AUDIT_ALLOW_UNRECORDED that this deployment records nothing.
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
		return nil, fmt.Errorf("runtime: audit writer unavailable, refusing to serve unrecorded: %w", err)
	}
	slog.InfoContext(ctx, "audit.writer_connected")
	return yb, nil
}

// buildKafkaRecorder opens the producer and proves the brokers answer. The
// franz-go client connects lazily, so construction succeeds against a dead
// broker; without the reachability check, an unreachable Kafka would start a
// server that fails every ledger write for as long as it runs.
func buildKafkaRecorder(ctx context.Context, cfg *config.Config, brokers []string) (audit.Recorder, error) {
	kafkaRec, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers:        brokers,
		Topic:          cfg.AuditKafkaTopic,
		ClientID:       cfg.AuditKafkaClientID,
		ProduceTimeout: cfg.AuditKafkaProduceTimeout,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.kafka_setup_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("runtime: audit producer unavailable, refusing to serve unrecorded: %w", err)
	}
	if err := waitForBrokers(ctx, cfg, kafkaRec); err != nil {
		_ = kafkaRec.CloseContext(ctx)
		return nil, fmt.Errorf("runtime: audit brokers unreachable, refusing to serve unrecorded: %w", err)
	}
	slog.InfoContext(ctx, "audit.kafka_enabled",
		slog.Int("broker_count", len(brokers)),
		slog.String("topic", cfg.AuditKafkaTopic),
	)
	return kafkaRec, nil
}

// waitForBrokers retries the reachability check until the configured budget
// runs out. One attempt is not enough on a cold start, where the broker comes
// up alongside the app; a budget distinguishes "not ready yet" from "down".
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
		lastErr = recorder.Ping(attemptCtx)
		cancelAttempt()
		if lastErr == nil {
			return nil
		}
		select {
		case <-deadlineCtx.Done():
			slog.ErrorContext(ctx, "audit.brokers_unreachable",
				slog.String("budget", budget.String()),
				slog.String("err", lastErr.Error()),
			)
			return fmt.Errorf("after %s: %w", budget, lastErr)
		case <-time.After(auditBrokerRetryInterval):
		}
	}
}

// unconfiguredAuditRecorder handles a deployment that names no audit backend.
// That is a configuration the operator has to mean, so it is refused unless
// AUDIT_ALLOW_UNRECORDED says otherwise, and when allowed it is recorded at
// error level, which is the one place in this file where a running server is
// deliberately unaudited.
func unconfiguredAuditRecorder(ctx context.Context, cfg *config.Config) (audit.Recorder, error) {
	if !cfg.AuditAllowUnrecorded {
		err := errors.New("set AUDIT_KAFKA_BROKERS or AUDIT_WRITER_DSN, or set AUDIT_ALLOW_UNRECORDED to run without a ledger")
		slog.ErrorContext(ctx, "audit.writer_unconfigured", slog.String("err", err.Error()))
		return nil, fmt.Errorf("runtime: no audit backend configured, refusing to serve unrecorded: %w", err)
	}
	slog.ErrorContext(ctx, "audit.unrecorded_acknowledged",
		slog.String("err", "AUDIT_ALLOW_UNRECORDED is set; this server records nothing"),
	)
	return audit.NoopRecorder{}, nil
}
