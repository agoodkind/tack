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

	"github.com/agoodkind/tack/internal/auth"
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

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			runMigrations(cfg)
			return
		case "seed":
			runSeed(cfg)
			return
		}
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

	// Repos
	issueRepo     := postgres.NewIssueRepo(pool)
	projectRepo   := postgres.NewProjectRepo(pool)
	workspaceRepo := postgres.NewWorkspaceRepo(pool)
	stateRepo     := postgres.NewStateRepo(pool)
	labelRepo     := postgres.NewLabelRepo(pool)
	epicRepo      := postgres.NewEpicRepo(pool)
	cycleRepo     := postgres.NewCycleRepo(pool)
	moduleRepo    := postgres.NewModuleRepo(pool)
	tokenRepo     := postgres.NewTokenRepo(pool)

	issueSvc := service.NewIssueService(
		issueRepo, projectRepo, workspaceRepo,
		fdbStores.Activity, fdbStores.Assignments, fdbStores.Labels,
	)
	projectSvc := service.NewProjectService(projectRepo, stateRepo)

	mcpHandler := mcpadapter.NewHandler(mcpadapter.Deps{
		Workspaces:  workspaceRepo,
		Projects:    projectRepo,
		ProjectSvc:  projectSvc,
		States:      stateRepo,
		Labels:      labelRepo,
		IssueSvc:    issueSvc,
		Epics:       epicRepo,
		Cycles:      cycleRepo,
		Modules:     moduleRepo,
		NodeTypes:   fdbStores.NodeTypes,
		Properties:  fdbStores.Properties,
		Activity:    fdbStores.Activity,
		Assignments: fdbStores.Assignments,
		NodeLabels:  fdbStores.Labels,
		Containment: fdbStores.Containment,
	})

	var authMiddleware func(http.Handler) http.Handler
	if cfg.Env == "development" {
		slog.Warn("running in dev auth mode — Bearer token is treated as a raw user UUID")
		authMiddleware = auth.DevBearer
	} else {
		authMiddleware = auth.Bearer(tokenRepo)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", authMiddleware(mcpHandler))
	mux.Handle("/mcp/", authMiddleware(mcpHandler))

	addr := fmt.Sprintf(":%d", cfg.Port)
	slog.Info("starting server", "addr", addr, "mcp_endpoint", addr+"/mcp", "env", cfg.Env)

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
