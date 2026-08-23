// Package runtime assembles the server object graph, including its datastores,
// audit runtime, node service, MCP handler, and auth middleware.
package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	mcpadapter "goodkind.io/tack/internal/adapters/mcp"
	"goodkind.io/tack/internal/adapters/postgres"
	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/config"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Graph is the assembled runtime object graph. Callers own it for the process
// lifetime and must call Close when done.
type Graph struct {
	MCPHandler     http.Handler
	AuthMiddleware func(http.Handler) http.Handler
	pool           *pgxpool.Pool
	fdbStores      *fdbadapter.Stores
	audit          auditRuntime
}

// BuildGraph opens the configured datastores and assembles the node service,
// MCP handler, and auth middleware. It also installs the process-global audit
// sinks. On failure it releases anything it already opened and returns the error.
func BuildGraph(ctx context.Context, cfg *config.Config) (*Graph, error) {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		slog.ErrorContext(ctx, "server.postgres_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("runtime: postgres: %w", err)
	}

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, pool)
	if err != nil {
		slog.ErrorContext(ctx, "server.foundationdb_failed", slog.String("err", err.Error()))
		pool.Close()
		return nil, fmt.Errorf("runtime: foundationdb: %w", err)
	}

	searcher := buildSearcher(cfg)
	auditRuntimeDeps := buildAuditRuntime(ctx, cfg)

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

	authMiddleware := buildAuthMiddleware(cfg, tokenRepo)

	return &Graph{
		MCPHandler:     mcpHandler,
		AuthMiddleware: authMiddleware,
		pool:           pool,
		fdbStores:      fdbStores,
		audit:          auditRuntimeDeps,
	}, nil
}

// PingYugabyte verifies that the Yugabyte connection pool can serve a request.
func (g *Graph) PingYugabyte(ctx context.Context) error {
	if err := g.pool.Ping(ctx); err != nil {
		slog.ErrorContext(ctx, "runtime.yugabyte_ping_failed", slog.String("err", err.Error()))
		return fmt.Errorf("ping yugabyte: %w", err)
	}
	return nil
}

// PingFoundationDB verifies that the FoundationDB stores can serve a request.
func (g *Graph) PingFoundationDB(ctx context.Context) error {
	if err := g.fdbStores.Ping(ctx); err != nil {
		slog.ErrorContext(ctx, "runtime.foundationdb_ping_failed", slog.String("err", err.Error()))
		return fmt.Errorf("ping foundationdb: %w", err)
	}
	return nil
}

// Close releases the audit runtime and the Postgres pool, in that order.
func (g *Graph) Close() {
	g.audit.Close()
	if g.pool != nil {
		g.pool.Close()
	}
}

func buildAuthMiddleware(cfg *config.Config, tokenRepo *postgres.TokenRepo) func(http.Handler) http.Handler {
	if cfg.Env == "development" {
		slog.Warn("dev_auth.enabled")
		return auth.DevBearer
	}
	return auth.Bearer(tokenRepo)
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
