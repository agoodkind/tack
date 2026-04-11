// Package search defines the Searcher interface for full-text search indexing
// and querying. The production implementation uses Meilisearch; a no-op stub
// is used when MEILI_URL is not configured or in tests.
package search

import "context"

// NodeDoc is the document shape stored and returned from the search index.
// All fields are populated at index time from NodeListView so that search
// results require zero follow-up FDB reads to render.
type NodeDoc struct {
	ID string `json:"id"`

	// Filterable attributes — kept as strings for Meilisearch filter syntax.
	OrgID       string `json:"org_id"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id,omitempty"`
	EntityType  string `json:"entity_type"`
	StateID     string `json:"state_id,omitempty"`
	Priority    string `json:"priority,omitempty"`
	IsDraft     bool   `json:"is_draft,omitempty"`

	// Searchable text fields.
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SequenceID  int32  `json:"sequence_id"`

	// Display fields — sufficient to render a list row without FDB reads.
	AssigneeIDs []string       `json:"assignee_ids,omitempty"`
	LabelIDs    []string       `json:"label_ids,omitempty"`
	EpicID      string         `json:"epic_id,omitempty"`
	StartDate   string         `json:"start_date,omitempty"`
	DueDate     string         `json:"due_date,omitempty"`
	UpdatedAt   string         `json:"updated_at"`
	CreatedAt   string         `json:"created_at"`
	CustomProps map[string]any `json:"custom_props,omitempty"`
}

// Searcher is the interface for full-text search indexing and querying.
// Implementations must be safe for concurrent use.
type Searcher interface {
	// Index adds or replaces a document in the named collection.
	// The id parameter must match the document's primary key field.
	Index(ctx context.Context, collection string, id string, doc any) error
	// Delete removes a document from the named collection.
	Delete(ctx context.Context, collection string, id string) error
	// Search returns NodeDocs matching query, scoped by equality filters,
	// along with facet counts keyed by field name → value → count.
	// Returns nil docs (not empty slice) to signal "unsupported"; callers
	// should fall back to FDB scan. Returns an empty non-nil slice when the
	// query matched nothing. Facets map may be nil if unsupported.
	Search(ctx context.Context, collection string, query string, filters map[string]string) ([]NodeDoc, map[string]map[string]int64, error)
}
