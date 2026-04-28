package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/meilisearch/meilisearch-go"
	domainsearch "goodkind.io/tack/internal/domain/search"
)

// Client is a Meilisearch-backed Searcher. It implements domain/search.Searcher.
type Client struct {
	meili meilisearch.ServiceManager
}

// New creates a Meilisearch Client connecting to url using masterKey.
func New(url, masterKey string) *Client {
	return &Client{
		meili: meilisearch.New(url, meilisearch.WithAPIKey(masterKey)),
	}
}

// EnsureIndex creates the named collection if it does not exist and sets the
// given fields as filterable attributes. Safe to call on every startup (idempotent).
func (c *Client) EnsureIndex(collection string, filterableAttributes []string) error {
	_, err := c.meili.CreateIndex(&meilisearch.IndexConfig{
		Uid:        collection,
		PrimaryKey: "id",
	})
	if err != nil {
		var meiliErr *meilisearch.Error
		if !errors.As(err, &meiliErr) || meiliErr.MeilisearchApiError.Code != "index_already_exists" {
			return fmt.Errorf("create index %s: %w", collection, err)
		}
	}
	attrs := make([]interface{}, len(filterableAttributes))
	for i, a := range filterableAttributes {
		attrs[i] = a
	}
	if _, err := c.meili.Index(collection).UpdateFilterableAttributes(&attrs); err != nil {
		return fmt.Errorf("set filterable attributes on %s: %w", collection, err)
	}
	return nil
}

// Index adds or replaces doc in collection, using "id" as the primary key.
func (c *Client) Index(_ context.Context, collection, _ string, doc *domainsearch.NodeDoc) error {
	pk := "id"
	if _, err := c.meili.Index(collection).AddDocuments([]any{doc}, &meilisearch.DocumentOptions{PrimaryKey: &pk}); err != nil {
		return fmt.Errorf("index document in %s: %w", collection, err)
	}
	return nil
}

// Delete removes the document with the given id from collection.
func (c *Client) Delete(_ context.Context, collection, id string) error {
	if _, err := c.meili.Index(collection).DeleteDocument(id, nil); err != nil {
		return fmt.Errorf("delete document %s from %s: %w", id, collection, err)
	}
	return nil
}

// Search returns NodeDocs matching query, scoped by equality filters, plus
// facet counts on every filterable attribute the index has configured.
//
// Facet field choice. We could hardcode a small list (org_id, node_type)
// here, but that drifts from EnsureIndex when callers add filterable
// attributes. Instead the search reads the live filterableAttributes off
// the index and asks Meilisearch to bucket on all of them. That keeps
// search results aligned with whatever the index actually allows.
//
// Returns a non-nil empty slice when the search succeeded but matched nothing.
// All NodeDoc fields are populated from the indexed document; no FDB reads needed.
func (c *Client) Search(_ context.Context, collection, query string, filters map[string]string) ([]domainsearch.NodeDoc, map[string]map[string]int64, error) {
	filterParts := make([]string, 0, len(filters))
	for k, v := range filters {
		filterParts = append(filterParts, fmt.Sprintf(`%s = "%s"`, k, v))
	}

	rawFacets, err := c.meili.Index(collection).GetFilterableAttributes()
	var facetFields []string
	if err == nil && rawFacets != nil {
		for _, v := range *rawFacets {
			if s, ok := v.(string); ok {
				facetFields = append(facetFields, s)
			}
		}
	}
	res, err := c.meili.Index(collection).Search(query, &meilisearch.SearchRequest{
		Filter: strings.Join(filterParts, " AND "),
		Limit:  200,
		Facets: facetFields,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("search %s: %w", collection, err)
	}

	docs := make([]domainsearch.NodeDoc, 0, len(res.Hits))
	for _, hit := range res.Hits {
		b, err := json.Marshal(hit)
		if err != nil {
			continue
		}
		var doc domainsearch.NodeDoc
		if err := json.Unmarshal(b, &doc); err != nil {
			continue
		}
		if doc.ID == "" {
			continue
		}
		docs = append(docs, doc)
	}

	facets := parseFacets(res.FacetDistribution)
	return docs, facets, nil
}

// parseFacets decodes Meilisearch's json.RawMessage facet distribution into
// map[string]map[string]int64 for clean consumption by callers.
func parseFacets(raw json.RawMessage) map[string]map[string]int64 {
	if len(raw) == 0 {
		return nil
	}
	var decoded map[string]map[string]float64
	if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded) == 0 {
		return nil
	}
	out := make(map[string]map[string]int64, len(decoded))
	for field, vals := range decoded {
		counts := make(map[string]int64, len(vals))
		for v, c := range vals {
			counts[v] = int64(c)
		}
		out[field] = counts
	}
	return out
}
