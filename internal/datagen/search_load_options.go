package datagen

import (
	"errors"
	"net/http"
	"time"
)

// SearchLoadOptions specifies the search workload parameters.
type SearchLoadOptions struct {
	Seed        int64
	Rate        int
	Duration    time.Duration
	Concurrency int
	Commit      bool
	// Endpoints lists base URLs for HTTP tack_search calls to deployed Tack
	// processes. The workload builds and calls an in-process runtime graph
	// when Endpoints is empty.
	Endpoints []string `exhaustruct:"optional"`
	// Client sends HTTP tack_search requests to Endpoints.
	Client *http.Client `exhaustruct:"optional"`
}

func validateSearchLoadOptions(options SearchLoadOptions) error {
	if options.Duration <= 0 {
		return errors.New("qa datagen search-load: duration must be positive")
	}
	if options.Concurrency <= 0 {
		return errors.New("qa datagen search-load: concurrency must be positive")
	}
	if options.Rate < 0 {
		return errors.New("qa datagen search-load: rate must not be negative")
	}
	if options.Commit && options.Rate == 0 {
		return errors.New("qa datagen search-load: rate must be positive with --commit")
	}
	if len(options.Endpoints) > 0 && options.Client == nil {
		return errors.New("qa datagen search-load: endpoints require an HTTP client")
	}
	return nil
}
