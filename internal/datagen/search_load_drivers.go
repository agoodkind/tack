package datagen

import (
	"context"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/runtime"
)

// searchLoadDrivers returns one remote driver per endpoint, or builds a new
// runtime graph and returns one in-process driver when Endpoints is empty. The
// returned function closes that graph.
func searchLoadDrivers(ctx context.Context, cfg *config.Config, options SearchLoadOptions) ([]*Driver, func(), error) {
	if len(options.Endpoints) == 0 {
		graph, err := runtime.BuildGraph(ctx, cfg)
		if err != nil {
			return nil, nil, loggedError(ctx, "qa datagen search-load: build runtime", err)
		}
		return []*Driver{NewDriver(graph, false, options.Seed)}, graph.Close, nil
	}
	drivers := make([]*Driver, 0, len(options.Endpoints))
	for _, endpoint := range options.Endpoints {
		driver, err := NewRemoteDriver(endpoint, options.Client, options.Seed)
		if err != nil {
			return nil, nil, loggedError(ctx, "qa datagen search-load: could not create a remote driver for "+endpoint, err)
		}
		drivers = append(drivers, driver)
	}
	return drivers, func() {}, nil
}
