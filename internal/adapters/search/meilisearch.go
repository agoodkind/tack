package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/meilisearch/meilisearch-go"
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
func (c *Client) Index(_ context.Context, collection, _ string, doc any) error {
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

// Search returns document IDs matching query, scoped by equality filters.
// Returns a non-nil empty slice when the search succeeded but matched nothing.
func (c *Client) Search(_ context.Context, collection, query string, filters map[string]string) ([]string, error) {
	filterParts := make([]string, 0, len(filters))
	for k, v := range filters {
		filterParts = append(filterParts, fmt.Sprintf(`%s = "%s"`, k, v))
	}

	res, err := c.meili.Index(collection).Search(query, &meilisearch.SearchRequest{
		Filter:               strings.Join(filterParts, " AND "),
		Limit:                200,
		AttributesToRetrieve: []string{"id"},
	})
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", collection, err)
	}

	ids := make([]string, 0, len(res.Hits))
	for _, hit := range res.Hits {
		raw, ok := hit["id"]
		if !ok {
			continue
		}
		var idStr string
		if err := json.Unmarshal(raw, &idStr); err != nil {
			continue
		}
		ids = append(ids, idStr)
	}
	return ids, nil
}
