package integration

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// TestSearchSummaryForbiddenNodeIsFoundAndWithheld reads one summary batch
// with allowed nodes of the caller's organization and one node of another
// organization. The foreign node must be found with current keys that the
// caller filter rejects. The test then rewrites the indexed access of the
// foreign node to the caller filter, and its pages pass the OpenSearch
// filter. Public search must still withhold it and return every allowed node
// once.
func TestSearchSummaryForbiddenNodeIsFoundAndWithheld(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	corpus := putPermissionCorpus(t, fixture)
	forbidden := corpus.Forbidden[0]
	requested := []uuid.UUID{corpus.Allowed[0], corpus.Allowed[1], forbidden, corpus.Allowed[2]}

	results, err := fixture.Stores.NodeSummaries(fixture.Stores.SearchPolicySet()).Summaries(t.Context(), requested, summaryNameBytes)
	if err != nil {
		t.Fatalf("read %d summaries: %v", len(requested), err)
	}
	if len(results) != len(requested) {
		t.Fatalf("summaries returned %d results for %d IDs", len(results), len(requested))
	}
	for position, result := range results {
		nodeID := requested[position]
		if result.NodeID != nodeID || result.Status != node.SummaryFound {
			t.Fatalf("result %d is node %s with status %q, want found node %s", position, result.NodeID, result.Status, nodeID)
		}
		permitted := corpus.Filter.Permits(result.AccessKeys)
		if nodeID == forbidden && (len(result.AccessKeys) == 0 || permitted) {
			t.Fatalf("foreign node %s returned keys %v with permitted=%t, want current keys that the caller filter rejects",
				nodeID, result.AccessKeys, permitted)
		}
		if nodeID != forbidden && !permitted {
			t.Fatalf("allowed node %s returned keys %v that the caller filter rejects", nodeID, result.AccessKeys)
		}
	}

	corruptIndexedAccess(t, fixture, forbidden, corpus.Filter)
	if !slices.Contains(rawRankedNodes(t, fixture, corpus.Filter, permissionQuery), forbidden) {
		t.Fatalf("the corrupted pages of node %s did not pass the OpenSearch filter", forbidden)
	}
	pages := callEverySearchPage(t, permissionQuery, fixture.Harness)
	if slices.Contains(pages.IDs, forbidden) {
		t.Fatalf("public search returned the foreign node %s", forbidden)
	}
	requireCorpusOnce(t, pages.IDs, corpus.Allowed, entryPoint(t, fixture, fixture.Workspaces[0]))
}
