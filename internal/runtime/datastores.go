package runtime

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

func openGraphStores(
	ctx context.Context,
	cfg *config.Config,
) (*pgxpool.Pool, *fdbadapter.Stores, error) {
	pool, err := postgres.NewPool(
		ctx,
		cfg.DatabaseURL,
		&telemetry.CountingQueryTracer{Next: &telemetry.QueryTracer{}},
	)
	if err != nil {
		slog.ErrorContext(ctx, "server.postgres_failed", slog.String("err", err.Error()))

		return nil, nil, fmt.Errorf("runtime: postgres: %w", err)
	}

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, pool)
	if err != nil {
		slog.ErrorContext(ctx, "server.foundationdb_failed", slog.String("err", err.Error()))
		pool.Close()

		return nil, nil, fmt.Errorf("runtime: foundationdb: %w", err)
	}

	return pool, fdbStores, nil
}
