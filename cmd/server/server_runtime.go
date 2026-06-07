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

// runServer wires the datastores, audit runtime, and MCP and Connect handlers,
// then serves until an interrupt arrives or the listener fails. It is the
// default action of the bare binary and of the explicit `serve` subcommand,
// and returns any fatal error to main rather than exiting in place.
func runServer(ctx context.Context, cfg *config.Config) error {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		slog.ErrorContext(ctx, "server.postgres_failed", slog.String("err", err.Error()))
		return fmt.Errorf("server: postgres: %w", err)
	}
	defer pool.Close()

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, pool)
	if err != nil {
		slog.ErrorContext(ctx, "server.foundationdb_failed", slog.String("err", err.Error()))
		return fmt.Errorf("server: foundationdb: %w", err)
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
		AuditQuerier:  auditRuntimeDeps.Querier,
		AuditRedactor: auditRuntimeDeps.Redactor,
	})

	authMiddleware := buildAuthMiddleware(cfg, tokenRepo)
	mux := buildServeMux(mcpHandler, authMiddleware)

	addr := fmt.Sprintf(":%d", cfg.Port)
	slog.InfoContext(ctx, "starting server",
		"addr", addr,
		"env", cfg.Env,
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
		Handler:           telemetry.RequestLogger(mux),
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
