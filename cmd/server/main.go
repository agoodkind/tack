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

	"goodkind.io/tack/gen/tack/v1/tackv1connect"
	"goodkind.io/tack/internal/auth"
	connectadapter "goodkind.io/tack/internal/adapters/connectrpc"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	mcpadapter "goodkind.io/tack/internal/adapters/mcp"
	"goodkind.io/tack/internal/adapters/postgres"
	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/temporal"
	"goodkind.io/tack/migrations"
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

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, pool)
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

	searcher := buildSearcher(cfg)

	// Repos (structural SQL only — no entity repos)
	projectRepo   := postgres.NewProjectRepo(pool)
	workspaceRepo := postgres.NewWorkspaceRepo(pool)
	stateRepo     := postgres.NewStateRepo(pool)
	labelRepo     := postgres.NewLabelRepo(pool)
	tokenRepo     := postgres.NewTokenRepo(pool)
	orgRepo       := postgres.NewOrgRepo(pool)
	userRepo      := postgres.NewUserRepo(pool)

	// Services
	noopAutomations := &node.NoopAutomationExecutor{}
	seeder    := service.NewWorkspaceSeeder(fdbStores.Properties, fdbStores.NodeTypes)
	issueSvc  := service.NewIssueService(
		fdbStores.Entities, fdbStores.Views, projectRepo,
		fdbStores.Activity, fdbStores.Assignments, fdbStores.Labels,
		fdbStores.Containment, fdbStores.NodeDeleter, nodeCleanup, searcher, noopAutomations,
	)
	projectSvc   := service.NewProjectService(projectRepo, stateRepo, searcher)
	epicSvc      := service.NewEpicService(
		fdbStores.Entities, fdbStores.Views,
		fdbStores.Assignments, fdbStores.Labels, fdbStores.Containment, searcher,
	)
	cycleSvc := service.NewCycleService(
		fdbStores.Entities, fdbStores.Views, fdbStores.Containment, searcher,
	)
	moduleSvc := service.NewModuleService(
		fdbStores.Entities, fdbStores.Views, fdbStores.Containment, searcher,
	)
	workspaceSvc := service.NewWorkspaceService(workspaceRepo, seeder, searcher, fdbStores.NodeTypes)

	mcpHandler := mcpadapter.NewHandler(mcpadapter.Deps{
		Workspaces:  workspaceRepo,
		Projects:    projectRepo,
		ProjectSvc:  projectSvc,
		States:      stateRepo,
		Labels:      labelRepo,
		IssueSvc:    issueSvc,
		EpicSvc:     epicSvc,
		CycleSvc:    cycleSvc,
		ModuleSvc:   moduleSvc,
		NodeTypes:   fdbStores.NodeTypes,
		Properties:  fdbStores.Properties,
		Activity:    fdbStores.Activity,
		Assignments: fdbStores.Assignments,
		NodeLabels:  fdbStores.Labels,
		Containment: fdbStores.Containment,
		Comments:    fdbStores.Comments,
		Orgs:        orgRepo,
		Users:       userRepo,
		Searcher:    searcher,
	})

	var authMiddleware func(http.Handler) http.Handler
	if cfg.Env == "development" {
		slog.Warn("running in dev auth mode -- Bearer token is treated as a raw user UUID")
		authMiddleware = auth.DevBearer
	} else {
		authMiddleware = auth.Bearer(tokenRepo)
	}

	// Connect-RPC handlers
	workspaceH := connectadapter.NewWorkspaceHandler(workspaceSvc)
	projectH   := connectadapter.NewProjectHandler(projectRepo, projectSvc)
	issueH     := connectadapter.NewIssueHandler(issueSvc)
	epicH      := connectadapter.NewEpicHandler(epicSvc)
	cycleH     := connectadapter.NewCycleHandler(cycleSvc)
	moduleH    := connectadapter.NewModuleHandler(moduleSvc)
	stateH     := connectadapter.NewStateHandler(stateRepo)
	labelH     := connectadapter.NewLabelHandler(labelRepo)
	activityH  := connectadapter.NewActivityHandler(issueSvc)

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

// buildSearcher creates a Meilisearch Client and ensures the nodes index is
// configured. Falls back to a no-op Searcher if setup fails (search degrades
// gracefully rather than blocking server startup).
func buildSearcher(cfg *config.Config) domainsearch.Searcher {
	meiliClient := searchadapter.New(cfg.MeiliURL, cfg.MeiliMasterKey)
	if err := meiliClient.EnsureIndex("nodes", []string{"org_id", "workspace_id", "project_id", "entity_type", "state_id", "priority", "is_draft"}); err != nil {
		slog.Error("meilisearch.setup_failed",
			slog.String("url", cfg.MeiliURL),
			slog.String("err", err.Error()),
		)
		return searchadapter.Noop{}
	}
	slog.Info("meilisearch.connected", slog.String("url", cfg.MeiliURL))
	return meiliClient
}
