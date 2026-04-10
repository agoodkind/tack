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

	"github.com/agoodkind/tack/gen/tack/v1/tackv1connect"
	"github.com/agoodkind/tack/internal/auth"
	connectadapter "github.com/agoodkind/tack/internal/adapters/connectrpc"
	fdbadapter "github.com/agoodkind/tack/internal/adapters/foundationdb"
	mcpadapter "github.com/agoodkind/tack/internal/adapters/mcp"
	"github.com/agoodkind/tack/internal/adapters/postgres"
	"github.com/agoodkind/tack/internal/config"
	"github.com/agoodkind/tack/internal/service"
	"github.com/agoodkind/tack/internal/telemetry"
	"github.com/agoodkind/tack/internal/temporal"
	"github.com/agoodkind/tack/migrations"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
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
		case "mcp-stdio":
			runMCPStdio(cfg)
			return
		}
	}

	runServer(cfg)
}

func runMCPStdio(cfg *config.Config) {
	ctx := context.Background()

	rawToken := os.Getenv("TACK_TOKEN")
	if rawToken == "" {
		slog.Error("mcp-stdio: TACK_TOKEN env var required")
		os.Exit(1)
	}

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

	tokenRepo := postgres.NewTokenRepo(pool)
	tok, err := tokenRepo.Validate(ctx, rawToken)
	if err != nil {
		slog.Error("mcp-stdio: invalid token", "err", err)
		os.Exit(1)
	}

	temporalClient, err := temporal.NewClient(cfg.TemporalAddress)
	if err != nil {
		slog.Error("temporal", "err", err)
		os.Exit(1)
	}
	defer temporalClient.Close()
	nodeCleanup := &temporal.NodeCleanupScheduler{Client: temporalClient}

	issueRepo     := postgres.NewIssueRepo(pool)
	projectRepo   := postgres.NewProjectRepo(pool)
	workspaceRepo := postgres.NewWorkspaceRepo(pool)
	stateRepo     := postgres.NewStateRepo(pool)
	labelRepo     := postgres.NewLabelRepo(pool)
	epicRepo      := postgres.NewEpicRepo(pool)
	cycleRepo     := postgres.NewCycleRepo(pool)
	moduleRepo    := postgres.NewModuleRepo(pool)

	issueSvc := service.NewIssueService(
		issueRepo, projectRepo, workspaceRepo,
		fdbStores.Activity, fdbStores.Assignments, fdbStores.Labels,
		fdbStores.NodeDeleter, nodeCleanup,
	)
	projectSvc := service.NewProjectService(projectRepo, stateRepo)

	handler := mcpadapter.NewHandler(mcpadapter.Deps{
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

	ctx = auth.WithUser(ctx, tok.UserID)
	s := handler.BuildServerForUser(ctx, tok.UserID)
	if err := s.Run(ctx, &mcp.StdioTransport{}); err != nil {
		slog.Error("mcp-stdio", "err", err)
		os.Exit(1)
	}
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

	// Temporal: client + worker
	temporalClient, err := temporal.NewClient(cfg.TemporalAddress)
	if err != nil {
		slog.Error("temporal", "err", err)
		os.Exit(1)
	}
	defer temporalClient.Close()

	acts := &temporal.Activities{NodeDeleter: fdbStores.NodeDeleter}
	w := temporal.NewWorker(temporalClient, acts)
	if err := w.Start(); err != nil {
		slog.Error("temporal worker", "err", err)
		os.Exit(1)
	}
	defer w.Stop()

	nodeCleanup := &temporal.NodeCleanupScheduler{Client: temporalClient}

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
		fdbStores.NodeDeleter, nodeCleanup,
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

	// Connect-RPC handlers
	workspaceH := connectadapter.NewWorkspaceHandler(workspaceRepo)
	projectH   := connectadapter.NewProjectHandler(projectRepo, projectSvc)
	issueH     := connectadapter.NewIssueHandler(issueSvc, fdbStores.Assignments)
	epicH      := connectadapter.NewEpicHandler(epicRepo)
	cycleH     := connectadapter.NewCycleHandler(cycleRepo, fdbStores.Containment)
	moduleH    := connectadapter.NewModuleHandler(moduleRepo, fdbStores.Containment)
	stateH     := connectadapter.NewStateHandler(stateRepo)
	labelH     := connectadapter.NewLabelHandler(labelRepo)
	activityH  := connectadapter.NewActivityHandler(fdbStores.Activity)

	mux := http.NewServeMux()

	// MCP Streamable HTTP
	mux.Handle("/mcp", authMiddleware(mcpHandler))
	mux.Handle("/mcp/", authMiddleware(mcpHandler))

	// Connect-RPC (speaks gRPC, gRPC-Web, and Connect protocols over HTTP/2)
	registerConnect := func(path string, handler http.Handler) {
		mux.Handle(path, authMiddleware(handler))
	}
	wPath, wHandler := tackv1connect.NewWorkspaceServiceHandler(workspaceH)
	registerConnect(wPath, wHandler)
	pPath, pHandler := tackv1connect.NewProjectServiceHandler(projectH)
	registerConnect(pPath, pHandler)
	iPath, iHandler := tackv1connect.NewIssueServiceHandler(issueH)
	registerConnect(iPath, iHandler)
	ePath, eHandler := tackv1connect.NewEpicServiceHandler(epicH)
	registerConnect(ePath, eHandler)
	cyPath, cyHandler := tackv1connect.NewCycleServiceHandler(cycleH)
	registerConnect(cyPath, cyHandler)
	moPath, moHandler := tackv1connect.NewModuleServiceHandler(moduleH)
	registerConnect(moPath, moHandler)
	stPath, stHandler := tackv1connect.NewStateServiceHandler(stateH)
	registerConnect(stPath, stHandler)
	lPath, lHandler := tackv1connect.NewLabelServiceHandler(labelH)
	registerConnect(lPath, lHandler)
	acPath, acHandler := tackv1connect.NewActivityServiceHandler(activityH)
	registerConnect(acPath, acHandler)

	addr := fmt.Sprintf(":%d", cfg.Port)
	slog.Info("starting server",
		"addr", addr,
		"mcp_endpoint", addr+"/mcp",
		"grpc_prefix", "/tack.v1.",
		"env", cfg.Env,
	)

	srv := &http.Server{
		Addr:    addr,
		Handler: h2c.NewHandler(telemetry.RequestLogger(mux), &http2.Server{}),
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
