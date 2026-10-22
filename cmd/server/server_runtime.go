package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"goodkind.io/tack/internal/config"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/version"
)

// runServer builds the shared runtime object graph, mounts the MCP and Connect
// handlers, then serves until an interrupt arrives or the listener fails. It is
// the default action of the bare binary and of the explicit `serve` subcommand,
// and returns any fatal error to main rather than exiting in place.
func runServer(ctx context.Context, cfg *config.Config) error {
	graph, err := appruntime.BuildGraph(ctx, cfg)
	if err != nil {
		return fmt.Errorf("server: build runtime: %w", err)
	}
	graph.StartSearchWorkers(ctx)
	graph.StartSearchModelRepair(ctx)
	graph.StartDeleteResumer(ctx)
	graph.StartAuditReadRecovery(ctx)
	defer graph.CloseContext(context.WithoutCancel(ctx))

	mux := graph.Routes
	registerConnectHandlers(mux, graph.AuthMiddleware)

	addr := fmt.Sprintf(":%d", cfg.Port)
	slog.InfoContext(ctx, "starting server",
		"addr", addr,
		"version", version.Tag(),
		"commit", version.Commit(),
		"build_hash", version.BuildHash(),
		"build_time", version.BuildTime(),
		"dirty", version.Dirty(),
	)

	// Enable HTTP/1.1 and unencrypted HTTP/2 (h2c) on the server directly,
	// the supported replacement for the deprecated x/net/http2/h2c handler.
	srv := &http.Server{
		Addr:              addr,
		Handler:           telemetry.RequestLogger(telemetry.RequestPathCounter(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv.Protocols = protocols

	serveErr := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(ctx, "http.panic", slog.Any("err", r))
			}
		}()
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-quit:
	case err := <-serveErr:
		slog.ErrorContext(ctx, "http.serve_failed", slog.String("err", err.Error()))
		return fmt.Errorf("http serve: %w", err)
	}

	slog.InfoContext(ctx, "shutting down")
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		slog.ErrorContext(ctx, "http.shutdown_failed", slog.String("err", err.Error()))
	}
	return nil
}
