package auditconsumer

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

const metricsHeaderTimeout = 10 * time.Second

func startMetricsServer(ctx context.Context, addr string) *http.Server {
	if addr == "" {
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle("/debug/vars", expvar.Handler())
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: metricsHeaderTimeout,
	}
	slog.InfoContext(ctx, "server.metrics_listening", slog.String("addr", addr))
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(
					ctx,
					"server.metrics_panic",
					slog.Any("panic", recovered),
					slog.String("err", fmt.Sprintf("%v", recovered)),
				)
			}
		}()
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.ErrorContext(ctx, "server.metrics_failed", slog.String("err", err.Error()))
		}
	}()

	return server
}
