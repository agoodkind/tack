// Package search defines the Searcher interface for full-text search indexing
// and querying. The production implementation uses Meilisearch; a no-op stub
// is used when MEILI_URL is not configured or in tests.
package search

import (
	"context"
	"encoding/json"
)

// NodeDoc is the generic document shape stored and returned from the search
// index. Only universal fields are first-class; everything concept-specific
// (priority, state_id, assignees, labels, etc.) lives in Props so no code path
// privileges one property over another. Props mirrors Node.Props as raw JSON
// so the search engine indexes whatever shape the property actually has.
type NodeDoc struct {
	ID       string                     `json:"id"`
	OrgID    string                     `json:"org_id"`
	NodeType string                     `json:"node_type"`
	Name     string                     `json:"name"`
	Props    map[string]json.RawMessage `json:"props,omitempty"`
}

// Searcher is the interface for full-text search indexing and querying. Only
// NodeDoc ever passes through; the typed signature replaces an earlier
// any-typed boundary so callers cannot accidentally hand a wrong shape to the
// index.
type Searcher interface {
	Index(ctx context.Context, collection string, id string, doc *NodeDoc) error
	Delete(ctx context.Context, collection string, id string) error
	// Search returns NodeDocs matching query, scoped by equality filters
	// (passed straight to the underlying engine), plus facet counts.
	Search(ctx context.Context, collection string, query string, filters map[string]string) ([]NodeDoc, map[string]map[string]int64, error)
}
