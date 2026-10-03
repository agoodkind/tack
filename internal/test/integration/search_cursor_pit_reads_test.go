package integration

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// requireStoredName reads the active page documents of nodeID under the point
// in time pitID with a term filter on node_id and requires each stored name
// to start with want, and each page text to contain want and not unwanted.
func requireStoredName(t *testing.T, fixture queryFixture, pitID string, nodeID uuid.UUID, want, unwanted string) {
	t.Helper()
	body := fmt.Sprintf(`{"size":100,"pit":{"id":%q,"keep_alive":"1m"},"query":{"bool":{"filter":[{"term":{"node_id":%q}},{"term":{"retired":false}}]}}}`, pitID, nodeID.String())
	response, err := fixture.Client.Search(t.Context(), &opensearchapi.SearchReq{Indices: nil, Body: strings.NewReader(body)})
	if err != nil {
		t.Fatalf("read pages of %s under point in time: %v", nodeID, err)
	}
	if len(response.Hits.Hits) == 0 {
		t.Fatalf("no active page of %s under point in time", nodeID)
	}
	for _, hit := range response.Hits.Hits {
		var page searchPageSource
		if err := json.Unmarshal(hit.Source, &page); err != nil || page.PageText == nil {
			t.Fatalf("decode page %s of %s: %v", hit.ID, nodeID, err)
		}
		if !strings.HasPrefix(page.Name, want) || !strings.Contains(*page.PageText, want) || strings.Contains(*page.PageText, unwanted) {
			t.Fatalf("page %s of %s stores name %q and text %q, want %q without %q", hit.ID, nodeID, page.Name, *page.PageText, want, unwanted)
		}
	}
}

// rawMatchesFrom reads every raw batch from the session sort position under
// the session point in time and returns the node ID of each page match in
// order.
func rawMatchesFrom(t *testing.T, ranker *search.QueryRanker, session searchdomain.Session) []uuid.UUID {
	t.Helper()
	snapshot := session.Snapshot
	after := session.Sort
	var nodes []uuid.UUID
	for range pitTraversalPages {
		batch, err := ranker.Read(t.Context(), session.Query, snapshot, after)
		if err != nil {
			t.Fatalf("read raw batch under the session point in time: %v", err)
		}
		if len(batch.Hits) == 0 {
			return nodes
		}
		snapshot.PITID = batch.PITID
		for _, hit := range batch.Hits {
			nodes = append(nodes, hit.NodeID)
		}
		after = batch.Hits[len(batch.Hits)-1].Sort
	}
	t.Fatalf("raw matches did not end within %d batches", pitTraversalPages)
	return nil
}

// distinctNodes returns the nodes of matches in first-match order, without
// repeats and without any node of excluded.
func distinctNodes(matches, excluded []uuid.UUID) []uuid.UUID {
	nodes := make([]uuid.UUID, 0, len(matches))
	for _, id := range matches {
		if !slices.Contains(nodes, id) && !slices.Contains(excluded, id) {
			nodes = append(nodes, id)
		}
	}
	return nodes
}

// continueSearch follows the session of first to completion and returns the
// node IDs of every later page.
func continueSearch(t *testing.T, harness *MCPHarness, first searchPage) []uuid.UUID {
	t.Helper()
	var continued []uuid.UUID
	cursor := first.Cursor
	for range pitTraversalPages {
		page := callSearch(t, harness, pitMutationQuery, cursor)
		continued = append(continued, page.IDs...)
		if page.Complete {
			return continued
		}
		cursor = page.Cursor
	}
	t.Fatalf("the established session did not complete within %d pages", pitTraversalPages)
	return nil
}

// requireSameNodes requires got to contain each node of want exactly once and
// no other node.
func requireSameNodes(t *testing.T, label string, got, want []uuid.UUID) {
	t.Helper()
	seen := make(map[uuid.UUID]bool, len(got))
	for _, id := range got {
		if seen[id] {
			t.Fatalf("%s returned node %s twice", label, id)
		}
		if !slices.Contains(want, id) {
			t.Fatalf("%s returned node %s outside the expected set", label, id)
		}
		seen[id] = true
	}
	if len(seen) != len(want) {
		t.Fatalf("%s returned %d nodes, want %d", label, len(seen), len(want))
	}
}
