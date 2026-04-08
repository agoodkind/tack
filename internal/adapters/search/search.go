package search

import "context"

// Searcher is the interface for full-text search indexing and querying.
// Day 0 is a no-op stub. Real Meilisearch client drops in with no service changes.
type Searcher interface {
	// Index adds or updates a document in the search index.
	Index(ctx context.Context, collection string, id string, doc any) error
	// Delete removes a document from the search index.
	Delete(ctx context.Context, collection string, id string) error
	// Search queries the index and returns matching document IDs.
	Search(ctx context.Context, collection string, query string) ([]string, error)
}

// Noop is a no-op Searcher for day 0. All operations succeed silently.
type Noop struct{}

func (Noop) Index(_ context.Context, _, _ string, _ any) error      { return nil }
func (Noop) Delete(_ context.Context, _, _ string) error             { return nil }
func (Noop) Search(_ context.Context, _, _ string) ([]string, error) { return nil, nil }
