package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/telemetry"
)

// recordServingSearchIndex stores the provisioned physical index in
// FoundationDB. Every search work claim targets that index, and page
// registration verifies it.
func recordServingSearchIndex(ctx context.Context, factory *cli.Factory, index string) error {
	env, err := NewEnv(ctx, factory.Cfg)
	if err != nil {
		wrapped := fmt.Errorf("open environment to record serving search index %s: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.provision.environment_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	defer env.Close()
	if err := env.Stores.InitializeSearchIndex(ctx, index); err != nil {
		wrapped := fmt.Errorf("record serving search index %s: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.provision.index_record_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	return nil
}

// readServingSearchIndex reads the serving physical index from FoundationDB.
// Search work claims read the same record, and an index replacement rewrites
// it when it switches the public alias.
func readServingSearchIndex(ctx context.Context, factory *cli.Factory) (string, error) {
	env, err := NewEnv(ctx, factory.Cfg)
	if err != nil {
		wrapped := fmt.Errorf("open environment to read serving search index: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.verify.environment_failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	defer env.Close()
	index, err := env.Stores.ServingSearchIndex(ctx)
	if err != nil {
		wrapped := fmt.Errorf("read serving search index: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.verify.index_record_failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	return index, nil
}
