// Package search provides implementations of the domain/search.Searcher interface.
// Noop is the zero-value stub used when Meilisearch is not configured.
// Client is the production Meilisearch-backed implementation.
package search

import (
	"context"

	domainsearch "goodkind.io/tack/internal/domain/search"
)

// Noop is a no-op Searcher. Index and Delete succeed silently.
// Search returns nil to signal "unsupported" so callers fall back to SQL.
type Noop struct{}

func (Noop) Index(_ context.Context, _, _ string, _ any) error { return nil }
func (Noop) Delete(_ context.Context, _, _ string) error       { return nil }
func (Noop) Search(_ context.Context, _, _ string, _ map[string]string) ([]domainsearch.NodeDoc, error) {
	return nil, nil
}
