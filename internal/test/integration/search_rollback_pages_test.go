package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

// rolloutPage represents one page document read from OpenSearch.
type rolloutPage struct {
	ID               string          `json:"-"`
	NodeID           uuid.UUID       `json:"node_id"`
	NodeRevision     string          `json:"node_revision"`
	PageText         *string         `json:"page_text"`
	SearchGeneration json.Number     `json:"search_generation"`
	Semantic         json.RawMessage `json:"page_text_semantic_info"`
	Access           struct {
		Versions   []string `json:"versions"`
		Keys       []string `json:"keys"`
		Generation int64    `json:"generation"`
	} `json:"access"`
}

// rolloutPages refreshes the index and returns each node's active pages
// in order.
func rolloutPages(t *testing.T, fixture rolloutFixture, nodes []uuid.UUID) []rolloutPage {
	t.Helper()
	if _, err := fixture.client.Indices.Refresh(rolloutContext(t), &opensearchapi.IndicesRefreshReq{Index: []string{fixture.index}}); err != nil {
		t.Fatalf("cannot refresh index %s: %v", fixture.index, err)
	}
	pages := make([]rolloutPage, 0, len(nodes)*2)
	for _, nodeID := range nodes {
		query := fmt.Sprintf(`{"size":1000,"query":{"bool":{"filter":[{"term":{"node_id":%q}},{"term":{"retired":false}}]}},"sort":[{"page_ordinal":"asc"}]}`, nodeID.String())
		response, err := fixture.client.Search(rolloutContext(t), &opensearchapi.SearchReq{Indices: []string{fixture.index}, Body: strings.NewReader(query)})
		if err != nil {
			t.Fatalf("cannot search pages of node %s: %v", nodeID, err)
		}
		for _, hit := range response.Hits.Hits {
			var page rolloutPage
			if err := json.Unmarshal(hit.Source, &page); err != nil {
				t.Fatalf("cannot decode page %s: %v", hit.ID, err)
			}
			page.ID = hit.ID
			pages = append(pages, page)
		}
	}
	if len(pages) == 0 {
		t.Fatal("the nodes do not have active pages")
	}
	return pages
}

func requirePageVersions(t *testing.T, pages []rolloutPage, versions []string) {
	t.Helper()
	for _, page := range pages {
		if !slices.Equal(page.Access.Versions, versions) {
			t.Fatalf("page %s stores versions %v instead of %v", page.ID, page.Access.Versions, versions)
		}
	}
}

// requirePagesPreserved requires every page to preserve its document ID,
// revision, text, and semantic fields byte for byte.
func requirePagesPreserved(t *testing.T, before, after []rolloutPage) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("active page count changed from %d to %d", len(before), len(after))
	}
	for position, page := range after {
		previous := before[position]
		if page.ID != previous.ID || page.NodeRevision != previous.NodeRevision || !bytes.Equal(page.Semantic, previous.Semantic) ||
			page.PageText == nil || previous.PageText == nil || *page.PageText != *previous.PageText {
			t.Fatalf("page %s changed its identity, text, or semantic fields", page.ID)
		}
	}
}
