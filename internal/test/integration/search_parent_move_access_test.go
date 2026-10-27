package integration_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// parentMovePage represents one active page document for a node in the serving
// index.
type parentMovePage struct {
	ID               string      `json:"-"`
	NodeRevision     string      `json:"node_revision"`
	PageText         *string     `json:"page_text"`
	SearchGeneration json.Number `json:"search_generation"`
	Access           struct {
		Keys []string `json:"keys"`
	} `json:"access"`
}

// TestSearchParentMoveUpdatesAccessOnly creates a leaf under the first entry
// point and indexes it. The test moves the leaf to the second entry point
// through NodeService.Update, changing only parent_id and scope_id. Every page
// must retain its document ID, node revision, and page_text and receive new
// access keys at a higher search generation. The test then updates the leaf's
// included property. Every page must have a new node revision and the new text.
func TestSearchParentMoveUpdatesAccessOnly(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	client := parentMoveClient(t, fixture)
	leaf := fixture.createLeaf(t, fixture.firstEntry, "quartz moved leaf")
	fixture.drain(t)
	before := parentMovePages(t, client, leaf)

	if err := fixture.moveLeaf(t, leaf, fixture.secondEntry); err != nil {
		t.Fatalf("move leaf %s to the second entry point: %v", leaf, err)
	}
	fixture.drain(t)
	moved := parentMovePages(t, client, leaf)
	if len(moved) != len(before) {
		t.Fatalf("the move changed the page count of leaf %s from %d to %d", leaf, len(before), len(moved))
	}
	for position, page := range moved {
		previous := before[position]
		if page.ID != previous.ID || page.NodeRevision != previous.NodeRevision || *page.PageText != *previous.PageText {
			t.Fatalf("page %d of leaf %s changed its document ID, node revision, or page_text", position, leaf)
		}
		if slices.Equal(page.Access.Keys, previous.Access.Keys) || parentMoveGeneration(t, page) <= parentMoveGeneration(t, previous) {
			t.Fatalf("page %d of leaf %s kept access %v at generation %s", position, leaf, page.Access.Keys, page.SearchGeneration)
		}
	}

	update := service.UpdateInput{
		NodeID: leaf, Props: map[string]json.RawMessage{fixture.kind.included: parentEdgeJSON(t, "quartz rewritten leaf")},
		ActorID: fixture.first.Actors[0].UserID,
	}
	if _, err := fixture.nodes.Update(integration.WithTestOperation(t, t.Context()), update); err != nil {
		t.Fatalf("update the included property of leaf %s: %v", leaf, err)
	}
	fixture.drain(t)
	var text strings.Builder
	for _, page := range parentMovePages(t, client, leaf) {
		if page.NodeRevision == moved[0].NodeRevision {
			t.Fatalf("page %s of leaf %s kept node revision %s after the content update", page.ID, leaf, page.NodeRevision)
		}
		text.WriteString(*page.PageText)
	}
	if !strings.Contains(text.String(), "quartz rewritten leaf") {
		t.Fatalf("pages of leaf %s after the content update have text %q, want the new text", leaf, text.String())
	}
}

// parentMoveClient opens an OpenSearch client using the fixture configuration's
// search settings.
func parentMoveClient(t *testing.T, fixture parentEdgeFixture) *opensearchapi.Client {
	t.Helper()
	certificate, err := config.LoadSearchCA(integration.WithTestOperation(t, t.Context()), fixture.cfg.SearchCA)
	if err != nil {
		t.Fatalf("load search CA: %v", err)
	}
	settings := search.Config{
		Endpoint: fixture.cfg.SearchEndpoint, CA: certificate, Username: fixture.cfg.SearchUsername,
		Password: fixture.cfg.SearchPassword, RequestTimeout: fixture.cfg.SearchRequestTimeout, MaxRetries: fixture.cfg.SearchMaxRetries,
	}
	baseAPI, err := opensearchapi.NewClient(opensearchapi.Config{Client: settings.ClientConfig(nil)})
	if err != nil {
		t.Fatalf("open OpenSearch client: %v", err)
	}
	return baseAPI
}

// parentMovePages refreshes the serving alias and returns the active pages of
// nodeID in page order. It copies the searchNodePages query from
// search_worker_fixture_test.go. The integration_test package cannot call
// searchNodePages.
func parentMovePages(t *testing.T, client *opensearchapi.Client, nodeID uuid.UUID) []parentMovePage {
	t.Helper()
	if _, err := client.Indices.Refresh(integration.WithTestOperation(t, t.Context()), &opensearchapi.IndicesRefreshReq{Indices: []string{search.PublicAlias}}); err != nil {
		t.Fatalf("refresh %s: %v", search.PublicAlias, err)
	}
	query := fmt.Sprintf(`{"size":1000,"query":{"bool":{"filter":[{"term":{"node_id":%q}},{"term":{"retired":false}}]}},"sort":[{"page_ordinal":"asc"}]}`, nodeID.String())
	response, err := client.Search(integration.WithTestOperation(t, t.Context()), &opensearchapi.SearchReq{Indices: []string{search.PublicAlias}, BodyReader: strings.NewReader(query)})
	if err != nil {
		t.Fatalf("search pages of node %s: %v", nodeID, err)
	}
	pages := make([]parentMovePage, 0, len(response.Hits.Hits))
	for _, hit := range response.Hits.Hits {
		if hit.ID == nil {
			t.Fatal("moved page requires a document ID")
		}
		var page parentMovePage
		if err := json.Unmarshal(hit.Source, &page); err != nil || page.PageText == nil {
			t.Fatalf("decode page %s of node %s: %v", *hit.ID, nodeID, err)
		}
		page.ID = *hit.ID
		pages = append(pages, page)
	}
	if len(pages) == 0 {
		t.Fatalf("node %s has no active pages in %s", nodeID, search.PublicAlias)
	}
	return pages
}

// parentMoveGeneration returns page's search generation as a number.
func parentMoveGeneration(t *testing.T, page parentMovePage) int64 {
	t.Helper()
	generation, err := page.SearchGeneration.Int64()
	if err != nil {
		t.Fatalf("parse the search generation %q of page %s: %v", page.SearchGeneration, page.ID, err)
	}
	return generation
}
