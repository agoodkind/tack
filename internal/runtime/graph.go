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
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/config"
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
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.CountingQueryTracer{Next: &telemetry.QueryTracer{}})
	if err != nil {
		slog.ErrorContext(ctx, "server.postgres_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("runtime: postgres: %w", err)
	}

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, pool)
	if err != nil {
		slog.ErrorContext(ctx, "server.foundationdb_failed", slog.String("err", err.Error()))
		pool.Close()
		return nil, fmt.Errorf("runtime: foundationdb: %w", err)
	}

	auditRuntimeDeps, err := buildAuditRuntime(ctx, cfg, fdbStores.OpsOutbox)
	if err != nil {
		pool.Close()
		return nil, err
	}

	// The token and membership caches take the two remaining ledger reads
	// off most requests (TACK-504, TACK-505); the lifetimes bound how long a
	// revocation or a membership change made elsewhere takes to reach this
	// instance.
	tokenRepo := auth.NewCachedTokenValidator(postgres.NewTokenRepo(pool),
		cfg.AuthTokenCacheLifetime, cfg.AuthTokenCacheSize)
	// Tool output renders who created or changed a node; the user cache keeps
	// that rendering off the ledger on most calls (criterion 12).
	userRepo := auth.NewCachedUsers(postgres.NewUserRepo(pool),
		cfg.AuthUserCacheLifetime, cfg.AuthUserCacheSize)
	orgMembers := auth.NewCachedMembers(postgres.NewOrgMemberRepo(pool),
		cfg.AuthMembershipCacheLifetime, cfg.AuthMembershipCacheSize)
	// A cold request reads the token and its holder's org set in one query,
	// so it costs one ledger read, not two (criterion 12).
	tokenRepo.PrimeMembers(orgMembers)

	nodeSvc := service.NewNodeService(
		fdbStores.Nodes,
		fdbStores.Views,
		fdbStores.NodeTypes,
		fdbStores.PropertyDefs,
		fdbStores.Relationships,
		fdbStores.NodeDeleter,
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
	})

	authMiddleware := buildAuthMiddleware(cfg, tokenRepo, orgMembers)

	return &Graph{
		MCPHandler:     mcpHandler,
		AuthMiddleware: authMiddleware,
		pool:           pool,
		fdbStores:      fdbStores,
		audit:          auditRuntimeDeps,
	}, nil
}

// PingYugabyte verifies that this instance can open a Yugabyte connection now.
// It dials outside the pool, because a pool slot can stay held for the
// driver's cleanup of a dead connection (see postgres.PingFreshConnection).
func (g *Graph) PingYugabyte(ctx context.Context) error {
	if err := postgres.PingFreshConnection(ctx, g.pool); err != nil {
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

func buildAuthMiddleware(cfg *config.Config, tokenRepo auth.TokenValidator, orgMembers auth.OrgLister) func(http.Handler) http.Handler {
	if cfg.Env == "development" {
		slog.Warn("dev_auth.enabled")
		return auth.DevBearer(orgMembers)
	}
	return auth.Bearer(tokenRepo, orgMembers)
}
