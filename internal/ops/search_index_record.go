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
