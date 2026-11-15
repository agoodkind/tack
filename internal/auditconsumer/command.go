package auditconsumer

import (
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/config"
)

// Execute loads settings and runs the audit-consumer process.
//
// It logs configuration failures and returns startup errors to the executable.
// A termination signal starts the bounded drain in Run.
//
// The executable delegates its entry point:
//
//	err := auditconsumer.Execute()
func Execute() error {
	cfg, err := config.LoadAuditConsumerSettings()
	if err != nil {
		wrapped := fmt.Errorf("load audit consumer configuration: %w", err)
		slog.Error("audit_consumer.config_failed", slog.String("err", wrapped.Error()))

		return wrapped
	}

	return Run(cfg)
}
