// Package main runs the audit consumer. It tails the Kafka topic the producer
// writes to, records each event in YugabyteDB audit.events, and runs the
// embedded notarizer.
package main

import (
	"log/slog"
	"os"

	"goodkind.io/tack/internal/auditconsumer"
)

func main() {
	err := auditconsumer.Execute()
	if err != nil {
		slog.Error("audit_consumer.exit", slog.Any("err", err))
		os.Exit(1)
	}
}
