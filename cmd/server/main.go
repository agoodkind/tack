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

	closer, err := telemetry.Setup(telemetry.LogConfig{
		Level:         cfg.LogLevel,
		JSONFile:      cfg.LogJSONFile,
		TextFile:      cfg.LogTextFile,
		DisableStdout: cfg.LogDisableStdout,
		MaxSizeMB:     cfg.LogMaxSizeMB,
		MaxBackups:    cfg.LogMaxBackups,
		MaxAgeDays:    cfg.LogMaxAgeDays,
	})
	if err != nil {
		slog.Error("telemetry.setup", "err", err)
		os.Exit(1)
	}
	defer func() {
		if closer != nil {
			_ = closer.Close()
		}
	}()
	slog.Info("server.startup",
		slog.String("env", cfg.Env),
		slog.String("version", version.Tag()),
		slog.String("commit", version.Commit()),
	)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			runMigrations(cfg)
			return
		case "seed":
			runSeed(cfg)
			return
		case "ops":
			runOps(cfg, os.Args[2:])
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

	searcher := buildSearcher(cfg)

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
	})

	var authMiddleware func(http.Handler) http.Handler
	if cfg.Env == "development" {
		slog.Warn("running in dev auth mode -- Bearer token is treated as a raw user UUID")
		authMiddleware = auth.DevBearer
	} else {
		authMiddleware = auth.Bearer(tokenRepo)
	}

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

	// expvar counters at /debug/vars. expvar.Handler returns the standard
	// JSON dump of every registered Var; tack's counters are registered via
	// internal/telemetry/metrics.go. Not gated on auth so a local watcher
	// can scrape it directly. Bind to localhost only when this matters.
	mux.Handle("/debug/vars", expvar.Handler())

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

// buildSearcher creates a Meilisearch client and ensures the nodes index is
// configured with a generic filterable set. Falls back to a no-op Searcher on
// setup failure.
func buildSearcher(cfg *config.Config) domainsearch.Searcher {
	meiliClient := searchadapter.New(cfg.MeiliURL, cfg.MeiliMasterKey)
	// Generic filterable attributes only. Per-property filterability is added
	// by callers at index/update time via PropertyDef.Indexed.
	if err := meiliClient.EnsureIndex("nodes", []string{"org_id", "node_type"}); err != nil {
		slog.Error("meilisearch.setup_failed",
			slog.String("url", cfg.MeiliURL),
			slog.String("err", err.Error()),
		)
		return searchadapter.Noop{}
	}
	slog.Info("meilisearch.connected", slog.String("url", cfg.MeiliURL))
	return meiliClient
}
