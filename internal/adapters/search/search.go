// Package search provides implementations of the domain/search.Searcher interface.
// Noop is the zero-value stub used when Meilisearch is not configured.
// Client is the production Meilisearch-backed implementation.
package search

import (
	"context"

	domainsearch "goodkind.io/tack/internal/domain/search"
)

// Noop is a no-op Searcher. Index and Delete succeed silently.
// Search and IndexBatch report the backend as unavailable with
// domainsearch.ErrUnavailable.
type Noop struct{}

func (Noop) Index(_ context.Context, _, _ string, _ *domainsearch.NodeDoc) error { return nil }

// IndexBatch reports the backend as unavailable, so a backfill against a
// missing Meilisearch fails instead of reporting success.
func (Noop) IndexBatch(_ context.Context, _ string, _ []*domainsearch.NodeDoc) error {
	return domainsearch.ErrUnavailable
}
func (Noop) Delete(_ context.Context, _, _ string) error { return nil }
func (Noop) Search(_ context.Context, _, _ string, _ map[string]string) ([]domainsearch.NodeDoc, map[string]map[string]int64, error) {
	return nil, nil, domainsearch.ErrUnavailable
}

// SearchVariants reports the backend as unavailable.
func (Noop) SearchVariants(_ context.Context, _ string, _ []string, _ map[string]string, _ int) ([]domainsearch.NodeDoc, error) {
	return nil, domainsearch.ErrUnavailable
}
