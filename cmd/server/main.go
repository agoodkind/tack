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

	fdbadapter "github.com/agoodkind/tack/internal/adapters/foundationdb"
	mcpadapter "github.com/agoodkind/tack/internal/adapters/mcp"
	"github.com/agoodkind/tack/internal/adapters/postgres"
	"github.com/agoodkind/tack/internal/config"
	"github.com/agoodkind/tack/internal/service"
	"github.com/agoodkind/tack/internal/telemetry"
	"github.com/agoodkind/tack/migrations"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	telemetry.Setup(telemetry.LogConfig{
		Level:      cfg.LogLevel,
		JSON:       cfg.Env == "production",
		File:       cfg.LogFile,
		MaxSizeMB:  cfg.LogMaxSizeMB,
		MaxBackups: cfg.LogMaxBackups,
		MaxAgeDays: cfg.LogMaxAgeDays,
	})

	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		runMigrations(cfg)
		return
	}

	runServer(cfg)
}

func runMigrations(cfg *config.Config) {
	ctx := context.Background()
	if err := postgres.Migrate(ctx, cfg.DatabaseURL, migrations.FS); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	slog.Info("migrations complete")
}

func runServer(cfg *config.Config) {
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		slog.Error("postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile)
	if err != nil {
		slog.Error("foundationdb", "err", err)
		os.Exit(1)
	}

	issueRepo := postgres.NewIssueRepo(pool)
	projectRepo := postgres.NewProjectRepo(pool)
	workspaceRepo := postgres.NewWorkspaceRepo(pool)
	tokenRepo := postgres.NewTokenRepo(pool)
	_ = tokenRepo

	issueSvc := service.NewIssueService(issueRepo, projectRepo, workspaceRepo, fdbStores.Activity)

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpadapter.NewHandler(issueSvc))
	mux.Handle("/mcp/", mcpadapter.NewHandler(issueSvc))

	addr := fmt.Sprintf(":%d", cfg.Port)
	slog.Info("starting server", "addr", addr, "mcp_endpoint", addr+"/mcp")

	srv := &http.Server{
		Addr:    addr,
		Handler: telemetry.RequestLogger(mux),
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
}
