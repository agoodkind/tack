// Command audit-consumer runs the audit consumer service.
package main

import (
	"log/slog"
	"os"

	"goodkind.io/tack/internal/command/auditconsumer"
)

func main() {
	err := auditconsumer.Run()
	if err != nil {
		slog.Error("audit_consumer.exit", slog.Any("err", err))
		os.Exit(1)
	}
}
