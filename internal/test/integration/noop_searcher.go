package integration

import (
	"context"

	domainsearch "goodkind.io/tack/internal/domain/search"
)

// noopSearcher is a domainsearch.Searcher that drops every call. Tests do not
// need real Meilisearch indexing, and the production noop adapter lives in a
// different package; mirroring the small interface here keeps the test
// dependencies tight.
type noopSearcher struct{}

func (noopSearcher) Index(_ context.Context, _, _ string, _ any) error { return nil }
func (noopSearcher) Delete(_ context.Context, _, _ string) error       { return nil }
func (noopSearcher) Search(_ context.Context, _, _ string, _ map[string]string) ([]domainsearch.NodeDoc, map[string]map[string]int64, error) {
	return nil, nil, nil
}
