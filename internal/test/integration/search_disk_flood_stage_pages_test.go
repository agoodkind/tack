package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

// storedPage is the generation and text that the serving index stores for one
// active page of a node.
type storedPage struct {
	Generation json.RawMessage `json:"search_generation"`
	PageText   string          `json:"page_text"`
}

// refreshServingIndex makes every accepted write of the serving index
// readable. The test runs no refresh while the index has the flood-stage
// block. The engine refuses every write during the block, and no new
// document exists to refresh.
func refreshServingIndex(t *testing.T, fixture queryFixture) error {
	t.Helper()
	_, err := fixture.Client.Indices.Refresh(t.Context(), &opensearchapi.IndicesRefreshReq{Index: []string{fixture.Index}})
	return clusterFailure("refresh "+fixture.Index, err)
}

// readStoredPages returns the active pages of nodeID in page order, as of the
// last refresh of the serving index. The sparse model relates different texts
// to each other. Only this exact read shows which text the index stores.
func readStoredPages(t *testing.T, fixture queryFixture, nodeID uuid.UUID) ([]storedPage, error) {
	t.Helper()
	query := fmt.Sprintf(`{"size":1000,"_source":["search_generation","page_text"],"query":{"bool":{"filter":[{"term":{"node_id":%q}},{"term":{"retired":false}}]}},"sort":[{"page_ordinal":"asc"}]}`, nodeID.String())
	response, err := fixture.Client.Search(t.Context(), &opensearchapi.SearchReq{Indices: []string{fixture.Index}, Body: strings.NewReader(query)})
	if err != nil {
		return nil, clusterFailure("read the stored pages of node "+nodeID.String(), err)
	}
	if response.Timeout || response.Shards.Failed > 0 {
		return nil, fmt.Errorf("read the stored pages of node %s: search timed out %t with %d failed shards", nodeID, response.Timeout, response.Shards.Failed)
	}
	pages := make([]storedPage, 0, len(response.Hits.Hits))
	for _, hit := range response.Hits.Hits {
		var page storedPage
		if err := json.Unmarshal(hit.Source, &page); err != nil {
			return nil, clusterFailure("decode page "+hit.ID+" of node "+nodeID.String(), err)
		}
		pages = append(pages, page)
	}
	return pages, nil
}

// logStoredPages logs the generation and text of every active page of nodeID.
func logStoredPages(t *testing.T, fixture queryFixture, nodeID uuid.UUID, when string) {
	t.Helper()
	pages, err := readStoredPages(t, fixture, nodeID)
	clusterRequire(t, "read the stored pages of node "+nodeID.String(), err)
	for position, page := range pages {
		t.Logf("%s: node %s page %d stores generation %s and text %q", when, nodeID, position, page.Generation, page.PageText)
	}
	if len(pages) == 0 {
		t.Logf("%s: node %s has no active page", when, nodeID)
	}
}

// storedTextError returns an error unless nodeID has at least one active page,
// every active page contains stored, and no active page contains absent.
func storedTextError(t *testing.T, fixture queryFixture, nodeID uuid.UUID, stored, absent string) error {
	t.Helper()
	pages, err := readStoredPages(t, fixture, nodeID)
	if err != nil {
		return err
	}
	if len(pages) == 0 {
		return fmt.Errorf("node %s has no active page, want text %q", nodeID, stored)
	}
	for position, page := range pages {
		if !strings.Contains(page.PageText, stored) || strings.Contains(page.PageText, absent) {
			return fmt.Errorf("node %s page %d stores generation %s and text %q, want %q and not %q", nodeID, position, page.Generation, page.PageText, stored, absent)
		}
	}
	return nil
}

// requireStoredText fails the test when storedTextError returns an error.
func requireStoredText(t *testing.T, fixture queryFixture, nodeID uuid.UUID, stored, absent string) {
	t.Helper()
	if err := storedTextError(t, fixture, nodeID, stored, absent); err != nil {
		t.Fatal(err)
	}
}

// requireNoStoredPages fails the test when nodeID has an active page.
func requireNoStoredPages(t *testing.T, fixture queryFixture, nodeID uuid.UUID) {
	t.Helper()
	pages, err := readStoredPages(t, fixture, nodeID)
	clusterRequire(t, "read the stored pages of node "+nodeID.String(), err)
	if len(pages) != 0 {
		t.Fatalf("node %s has %d active pages, want none; first page stores generation %s and text %q", nodeID, len(pages), pages[0].Generation, pages[0].PageText)
	}
}
