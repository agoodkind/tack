package runtime

import (
	"context"
	"errors"
	"log/slog"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

// buildSearchQuery constructs the ranker, session store, summary reader,
// and search service behind tack_search. While OPENSEARCH_PUBLIC_ENABLED is
// false it returns a binding without a runner, and tack_search keeps the
// fixed unavailable response. Index workers run in either state.
func buildSearchQuery(ctx context.Context, cfg *config.Config, stores *fdbadapter.Stores, search searchRuntime) (tools.SearchBinding, error) {
	disabled := tools.SearchBinding{Runner: nil, Cursors: nil}
	if !cfg.SearchPublicEnabled {
		telemetry.L(ctx).InfoContext(ctx, "search.public.disabled")
		return disabled, nil
	}
	if search.adapter == nil {
		return disabled, searchRuntimeFailure(ctx, "enable public search", errors.New("OPENSEARCH_PUBLIC_ENABLED requires OPENSEARCH_ENDPOINT"))
	}
	settings, err := config.LoadSearchQuerySettings(ctx)
	if err != nil {
		return disabled, searchRuntimeFailure(ctx, "load search query settings", err)
	}
	cursorKey, err := settings.CursorKeyBytes()
	if err != nil {
		return disabled, searchRuntimeFailure(ctx, "decode search cursor key", err)
	}
	source := clock.Wall{}
	policies := stores.SearchPolicySet()
	sessions := stores.SearchSessions(source, settings.IdleTimeout)
	ranker := search.adapter.Ranker(searchadapter.RankerSettings{
		KeepAlive: settings.IdleTimeout, TokenBytes: settings.MaxTokenBytes, BatchSize: settings.BatchSize,
	})
	runner := service.NewSearchQueryService(service.SearchQueryPorts{
		Ranker: ranker, Sessions: sessions, Expired: sessions,
		Summaries: stores.NodeSummaries(policies), Access: policies, Index: stores,
	}, source, settings)
	telemetry.L(ctx).InfoContext(ctx, "search.public.enabled", slog.Int("max_results", settings.MaxResults),
		slog.Int("max_batches", settings.MaxBatches), slog.Duration("idle_timeout", settings.IdleTimeout))
	return tools.SearchBinding{Runner: runner, Cursors: tools.NewSearchCursorCodec(cursorKey)}, nil
}
