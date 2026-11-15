package auditconsumer

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"goodkind.io/tack/internal/audit"
)

func drainConsumer(metricsServer *http.Server, relay *audit.Relay, consumer *audit.Consumer) {
	slog.Info("audit_consumer.draining")
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer drainCancel()
	if metricsServer != nil {
		metricsCtx, metricsCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := metricsServer.Shutdown(metricsCtx); err != nil {
			slog.Error("server.metrics_shutdown_failed", slog.String("err", err.Error()))
		}

		metricsCancel()
	}

	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error(
					"audit_consumer.close_panic",
					slog.Any("panic", r),
					slog.String("err", fmt.Sprintf("%v", r)),
				)
			}

			close(done)
		}()
		if err := relay.Close(); err != nil {
			slog.Error("audit_consumer.relay_close_failed", slog.String("err", err.Error()))
		}

		if err := consumer.Close(); err != nil {
			slog.Error("audit_consumer.consumer_close_failed", slog.String("err", err.Error()))
		}
	}()
	select {
	case <-done:
		slog.Info("audit_consumer.stopped")
	case <-drainCtx.Done():
		slog.Warn("audit_consumer.drain_timeout")
	}
}
