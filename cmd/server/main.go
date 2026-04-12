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

	"goodkind.io/tack/internal/auth"
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

	// Auth repos (SQL only — users, api_tokens stay in SQL)
	tokenRepo := postgres.NewTokenRepo(pool)
	userRepo  := postgres.NewUserRepo(pool)
	orgMembers := postgres.NewOrgMemberRepo(pool)

	// Services
	noopAutomations := &node.NoopAutomationExecutor{}
	seeder    := service.NewWorkspaceSeeder(fdbStores.Properties, fdbStores.NodeTypes)
	nodeSvc   := service.NewNodeService(
		fdbStores.Entities, fdbStores.Views, fdbStores.NodeTypes, fdbStores.Properties,
		fdbStores.Activity, fdbStores.Assignments, fdbStores.Labels,
		fdbStores.Containment, fdbStores.NodeDeleter, nodeCleanup, searcher, noopAutomations,
	)
	_ = seeder // used by seed command

	mcpHandler := mcpadapter.NewHandler(mcpadapter.Deps{
		NodeSvc:    nodeSvc,
		Entities:   fdbStores.Entities,
		Reader:     fdbStores.Views,
		NodeTypes:  fdbStores.NodeTypes,
		Properties: fdbStores.Properties,
		Comments:   fdbStores.Comments,
		Members:    orgMembers,
		Users:      userRepo,
		Searcher:   searcher,
	})

	var authMiddleware func(http.Handler) http.Handler
	if cfg.Env == "development" {
		slog.Warn("running in dev auth mode -- Bearer token is treated as a raw user UUID")
		authMiddleware = auth.DevBearer
	} else {
		authMiddleware = auth.Bearer(tokenRepo)
	}

	mux := http.NewServeMux()

	// MCP Streamable HTTP
	mcpWithAuth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed: streamable-http stateless mode requires POST", http.StatusMethodNotAllowed)
			return
		}
		authMiddleware(mcpHandler).ServeHTTP(w, r)
	})
	mux.Handle("/mcp", mcpWithAuth)
	mux.Handle("/mcp/", mcpWithAuth)

	// TODO: generic NodeService Connect-RPC handler (TACK-74)

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
