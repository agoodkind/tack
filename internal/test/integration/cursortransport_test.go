package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/test/integration"
)

// parentMoveClient opens an OpenSearch client using the fixture configuration's
// search settings.
func parentMoveClient(t *testing.T, fixture parentEdgeFixture) *opensearchapi.Client {
	t.Helper()
	certificate, err := config.LoadSearchCA(
		integration.WithTestOperation(t, t.Context()),
		fixture.cfg.SearchCA,
	)
	if err != nil {
		t.Fatalf("load search CA: %v", err)
	}

	settings := search.Config{
		Endpoint:       fixture.cfg.SearchEndpoint,
		CA:             certificate,
		Username:       fixture.cfg.SearchUsername,
		Password:       fixture.cfg.SearchPassword,
		RequestTimeout: fixture.cfg.SearchRequestTimeout,
		MaxRetries:     fixture.cfg.SearchMaxRetries,
	}
	baseAPI, err := opensearchapi.NewClient(opensearchapi.Config{
		Client: settings.ClientConfig(nil),
	})
	if err != nil {
		t.Fatalf("open OpenSearch client: %v", err)
	}

	return baseAPI
}

// parentMovePages refreshes the serving alias and returns the active pages of
// nodeID in page order. It copies the searchNodePages query from
// search_worker_fixture_test.go. The integration_test package cannot call
// searchNodePages.
func parentMovePages(
	t *testing.T,
	client *opensearchapi.Client,
	nodeID uuid.UUID,
) []parentMovePage {
	t.Helper()
	if _, err := client.Indices.Refresh(
		integration.WithTestOperation(t, t.Context()),
		&opensearchapi.IndicesRefreshReq{Indices: []string{search.PublicAlias}},
	); err != nil {
		t.Fatalf("refresh %s: %v", search.PublicAlias, err)
	}

	query := fmt.Sprintf(
		`{"size":1000,"query":{"bool":{"filter":[{"term":{"node_id":%q}},{"term":{"retired":false}}]}},"sort":[{"page_ordinal":"asc"}]}`,
		nodeID.String(),
	)
	response, err := client.Search(
		integration.WithTestOperation(t, t.Context()),
		&opensearchapi.SearchReq{
			Indices:    []string{search.PublicAlias},
			BodyReader: strings.NewReader(query),
		},
	)
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
		t.Fatalf(
			"parse the search generation %q of page %s: %v",
			page.SearchGeneration,
			page.ID,
			err,
		)
	}

	return generation
}
