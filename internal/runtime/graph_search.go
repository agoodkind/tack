package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

// searchRuntime is the adapter, workers, and idle interval of the durable
// indexing loops.
type searchRuntime struct {
	adapter   *searchadapter.Adapter
	workers   []*service.SearchWorker
	idleTime  time.Duration
	retention *service.SearchRetention
	repair    *service.SearchModelRepair
}

// buildSearchRuntime constructs the content reader, policy set, work store,
// access store, adapter, and workers on the graph's clock. It returns an
// empty runtime when no search endpoint is configured.
func buildSearchRuntime(
	ctx context.Context,
	cfg *config.Config,
	stores *fdbadapter.Stores,
	source clock.Clock,
) (searchRuntime, error) {
	empty := searchRuntime{adapter: nil, workers: nil, idleTime: 0, retention: nil, repair: nil}
	if cfg.SearchEndpoint == "" {
		return empty, nil
	}

	settings, err := config.LoadSearchWorkerSettings(ctx)
	if err != nil {
		return empty, searchRuntimeFailure(ctx, "load search worker settings", err)
	}

	retentionSettings, err := config.LoadSearchRetentionSettings(ctx)
	if err != nil {
		return empty, searchRuntimeFailure(ctx, "load search retention settings", err)
	}

	topology, err := config.LoadSearchTopology(ctx)
	if err != nil {
		return empty, searchRuntimeFailure(ctx, "load search topology", err)
	}

	repairSettings, err := config.LoadSearchModelRepairSettings(ctx)
	if err != nil {
		return empty, searchRuntimeFailure(ctx, "load search model repair settings", err)
	}

	certificate, err := config.LoadSearchCA(ctx, cfg.SearchCA)
	if err != nil {
		return empty, searchRuntimeFailure(ctx, "load search CA", err)
	}

	pass := cfg.SearchPassword
	adapter, err := searchadapter.New(
		ctx,
		searchadapter.Config{
			Endpoint:       cfg.SearchEndpoint,
			CA:             certificate,
			Username:       cfg.SearchUsername,
			Password:       pass,
			RequestTimeout: cfg.SearchRequestTimeout,
			MaxRetries:     cfg.SearchMaxRetries,
		},
	)
	if err != nil {
		return empty, searchRuntimeFailure(ctx, "create OpenSearch adapter", err)
	}

	policies := stores.SearchPolicySet()
	rollouts := stores.SearchRollouts(source, policies)
	rebuilds := stores.SearchRebuilds(source)
	ports := service.SearchWorkerPorts{
		Store:     stores.SearchWork(source),
		Reader:    stores.SearchContent(policies),
		Access:    stores.SearchAccess(policies),
		Writer:    adapter,
		Rollouts:  rollouts,
		Sessions:  rollouts,
		Documents: adapter,
		Rebuilds:  rebuilds,
		Replacer:  adapter,
	}
	retention := service.NewSearchRetention(
		rebuilds,
		adapter,
		stores,
		source,
		retentionSettings,
		topology,
	)

	workers, err := buildSearchWorkers(ctx, ports, source, settings, adapter)
	if err != nil {
		return empty, err
	}

	repair := service.NewSearchModelRepair(
		service.SearchModelRepairPorts{
			Engine:  adapter,
			Records: stores.SearchModelRepairs(),
			Index:   stores,
		},
		source,
		repairSettings,
		service.SearchModelRepairGates{
			Enabled:      repairSettings.Enabled,
			PublicSearch: cfg.SearchPublicEnabled,
		},
	)

	return searchRuntime{
		adapter:   adapter,
		workers:   workers,
		idleTime:  settings.IdleInterval,
		retention: retention,
		repair:    repair,
	}, nil
}

func searchRuntimeFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("runtime: %s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(
		ctx,
		"search.runtime.build_failed",
		slog.String("err", wrapped.Error()),
	)

	return wrapped
}
