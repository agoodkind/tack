package main

import (
	"context"
	"expvar"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	mcpadapter "goodkind.io/tack/internal/adapters/mcp"
	"goodkind.io/tack/internal/adapters/postgres"
	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/config"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/version"
)

func fatalExit() {
	os.Exit(1)
}

// runServer wires the datastores, audit runtime, and MCP and Connect handlers,
// then serves until an interrupt arrives. It is the default action of the bare
// binary and of the explicit `serve` subcommand.
func runServer(cfg *config.Config) {
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		slog.Error("postgres", "err", err)
		fatalExit()
	}
	defer pool.Close()

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, pool)
	if err != nil {
		slog.Error("foundationdb", "err", err)
		fatalExit()
	}

	searcher := buildSearcher(cfg)

	auditRuntimeDeps := setupAuditRuntime(ctx, cfg)
	defer auditRuntimeDeps.Close()
	tokenRepo := postgres.NewTokenRepo(pool)
	userRepo := postgres.NewUserRepo(pool)
	orgMembers := postgres.NewOrgMemberRepo(pool)

	nodeSvc := service.NewNodeService(
		fdbStores.Nodes,
		fdbStores.Views,
		fdbStores.NodeTypes,
		fdbStores.PropertyDefs,
		fdbStores.Relationships,
		fdbStores.NodeDeleter,
		searcher,
	)

	mcpHandler := mcpadapter.NewHandler(mcpadapter.Deps{
		NodeSvc:       nodeSvc,
		Nodes:         fdbStores.Nodes,
		Reader:        fdbStores.Views,
		NodeTypes:     fdbStores.NodeTypes,
		PropertyDefs:  fdbStores.PropertyDefs,
		Relationships: fdbStores.Relationships,
		Members:       orgMembers,
		Users:         userRepo,
		Searcher:      searcher,
		AuditReader:   auditRuntimeDeps.Reader,
		AuditRedactor: auditRuntimeDeps.Redactor,
	})

	authMiddleware := buildAuthMiddleware(cfg, tokenRepo)
	mux := buildServeMux(mcpHandler, authMiddleware)

	addr := fmt.Sprintf(":%d", cfg.Port)
	slog.Info("starting server",
		"addr", addr,
		"env", cfg.Env,
		"version", version.Tag(),
		"commit", version.Commit(),
		"build_hash", version.BuildHash(),
		"build_time", version.BuildTime(),
		"dirty", version.Dirty(),
	)

	srv := &http.Server{
		Addr:              addr,
		Handler:           h2c.NewHandler(telemetry.RequestLogger(mux), &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("http.panic", slog.Any("err", r))
			}
		}()
		err := srv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			slog.Error("http", "err", err)
			fatalExit()
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

func buildAuthMiddleware(cfg *config.Config, tokenRepo *postgres.TokenRepo) func(http.Handler) http.Handler {
	if cfg.Env == "development" {
		slog.Warn("dev_auth.enabled")
		return auth.DevBearer
	}
	return auth.Bearer(tokenRepo)
}

func buildServeMux(mcpHandler http.Handler, authMiddleware func(http.Handler) http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
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
	registerConnectHandlers(mux, authMiddleware)

	// expvar counters at /debug/vars. Not gated on auth so a local watcher can
	// scrape it directly; bind to localhost only when this matters.
	mux.Handle("/debug/vars", expvar.Handler())
	return mux
}

// buildSearcher creates a Meilisearch client and ensures the nodes index is
// configured with a generic filterable set. Falls back to a no-op Searcher on
// setup failure.
func buildSearcher(cfg *config.Config) domainsearch.Searcher {
	meiliClient := searchadapter.New(cfg.MeiliURL, cfg.MeiliMasterKey)
	err := meiliClient.EnsureIndex("nodes", []string{"org_id", "node_type"})
	if err != nil {
		slog.Error("meilisearch.setup_failed",
			slog.String("url", cfg.MeiliURL),
			slog.String("err", err.Error()),
		)
		return searchadapter.Noop{}
	}
	slog.Info("meilisearch.connected", slog.String("url", cfg.MeiliURL))
	return meiliClient
}
