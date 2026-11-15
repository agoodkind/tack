// Package main runs the audit consumer. It tails the Kafka topic the producer
// writes to, records each event in YugabyteDB audit.events, and runs the
// embedded notarizer.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"goodkind.io/tack/internal/auditconsumer"
	"goodkind.io/tack/internal/config"
)

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

	return auditconsumer.Run(cfg)
}
