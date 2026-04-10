// Package search defines the Searcher interface for full-text search indexing
// and querying. The production implementation uses Meilisearch; a no-op stub
// is used when MEILI_URL is not configured or in tests.
package search

import "context"

// NodeDoc is the document shape stored and returned from the search index.
type NodeDoc struct {
	ID          string `json:"id"`
	NodeID      string `json:"node_id,omitempty"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id,omitempty"`
	EntityType  string `json:"entity_type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Searcher is the interface for full-text search indexing and querying.
// Implementations must be safe for concurrent use.
type Searcher interface {
	// Index adds or replaces a document in the named collection.
	// The id parameter must match the document's primary key field.
	Index(ctx context.Context, collection string, id string, doc any) error
	// Delete removes a document from the named collection.
	Delete(ctx context.Context, collection string, id string) error
	// Search returns NodeDocs matching query, scoped by equality filters.
	// Returns nil (not an empty slice) to signal "unsupported"; callers should
	// fall back to SQL. Returns an empty non-nil slice when the query matched nothing.
	Search(ctx context.Context, collection string, query string, filters map[string]string) ([]NodeDoc, error)
}
