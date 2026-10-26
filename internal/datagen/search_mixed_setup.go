package datagen

import (
	"context"
	"fmt"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/runtime"
)

// searchMixedMinimumTenants is the number of organizations that the tenant isolation checks need.
const searchMixedMinimumTenants = 2

// newSearchMixed opens the stores and readers, bootstraps the tenants, builds one driver per
// endpoint, creates the tenant projects and the search corpus, and builds the search loads.
// The corpus index waits end when stopContext ends and record each completed wait in corpusIndex.
// Every call uses ctx.
func newSearchMixed(
	stopContext, ctx context.Context, cfg *config.Config, options SearchMixedOptions, plannedSearches int,
	corpusIndex *searchMixedCorpusIndex,
) (*searchMixed, func(), error) {
	limits, err := config.LoadSearchQuerySettings(ctx)
	if err != nil {
		return nil, nil, loggedError(ctx, "qa datagen search-mixed: the command could not load the search settings", err)
	}
	graph, err := runtime.BuildGraph(ctx, cfg)
	if err != nil {
		return nil, nil, loggedError(ctx, "qa datagen search-mixed: the command could not build the runtime", err)
	}
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, nil)
	if err != nil {
		graph.Close()
		return nil, nil, loggedError(ctx, "qa datagen search-mixed: the command could not open FoundationDB", err)
	}
	backlog, err := newSearchMixedBacklog(ctx, cfg)
	if err != nil {
		graph.Close()
		return nil, nil, err
	}
	resources, err := newSearchMixedResources(ctx, cfg)
	if err != nil {
		graph.Close()
		return nil, nil, err
	}
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, nil)
	if err != nil {
		resources.close(ctx)
		graph.Close()
		return nil, nil, loggedError(ctx, "qa datagen search-mixed: the command could not connect to YugabyteDB for the revocation check", err)
	}
	closeAll := func() {
		pool.Close()
		resources.close(ctx)
		graph.Close()
	}
	drivers, err := searchMixedDrivers(ctx, graph, options)
	if err != nil {
		closeAll()
		return nil, nil, err
	}
	mixed := &searchMixed{
		cfg: cfg, options: options, drivers: drivers, tenants: nil, loads: nil, loadStats: &searchLoadStats{},
		runKey: opaqueKey("mix"), readWord: opaqueKey("read"), victimWord: opaqueKey("victim"),
		limits: searchLimits{MaxResults: limits.MaxResults, MaxResponseBytes: limits.MaxResponseBytes},
		stores: stores, pages: graph, backlog: backlog, resources: resources, members: postgres.NewOrgMemberRepo(pool),
		stats: &searchMixedStats{}, phases: &searchMixedPhaseStats{skipped: []string{}}, corpusIndex: corpusIndex,
	}
	if err := mixed.prepare(stopContext, ctx, plannedSearches); err != nil {
		closeAll()
		return nil, nil, err
	}
	return mixed, closeAll, nil
}

func (m *searchMixed) prepare(stopContext, ctx context.Context, plannedSearches int) error {
	if _, err := m.resources.read(ctx); err != nil {
		return err
	}
	scale, err := ParseScale(searchMixedScale)
	if err != nil {
		return err
	}
	identities, err := BootstrapIdentities(ctx, m.cfg, m.options.Seed, scale)
	if err != nil {
		return err
	}
	m.tenants = selectSearchMixedTenants(identities)
	if len(m.tenants) < searchMixedMinimumTenants || len(m.tenants[0].workspace.Actors) <= revokedActorIndex {
		return fmt.Errorf("qa datagen search-mixed: scale %s created %d tenants; the run requires %d tenants and more than %d actors in the first tenant",
			searchMixedScale, len(m.tenants), searchMixedMinimumTenants, revokedActorIndex)
	}
	if m.options.Concurrency < len(m.tenants) {
		return fmt.Errorf("qa datagen search-mixed: concurrency %d is less than the tenant count %d; set --concurrency to at least the tenant count", m.options.Concurrency, len(m.tenants))
	}
	if err := m.createProjects(ctx); err != nil {
		return err
	}
	if err := m.createCorpus(stopContext, ctx); err != nil {
		return err
	}
	return m.newSearchMixedLoads(ctx, plannedSearches)
}

// searchMixedDrivers returns one remote driver per endpoint. It returns one in-process driver
// when the run does not specify endpoints.
func searchMixedDrivers(ctx context.Context, graph *runtime.Graph, options SearchMixedOptions) ([]*Driver, error) {
	if len(options.Endpoints) == 0 {
		return []*Driver{NewDriver(graph, false, options.Seed)}, nil
	}
	drivers := make([]*Driver, 0, len(options.Endpoints))
	for _, endpoint := range options.Endpoints {
		driver, err := NewRemoteDriver(endpoint, options.Client, options.Seed)
		if err != nil {
			return nil, loggedError(ctx, "qa datagen search-mixed: the command could not build a remote driver for "+endpoint, err)
		}
		drivers = append(drivers, driver)
	}
	return drivers, nil
}
