package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// TestSearchSummaryBatchReturnsDeletedInPlace reads one summary batch with a
// deleted node between two found nodes. The deleted node must return status
// deleted at its input position with no keys, and the batch must still use
// one FoundationDB transaction.
func TestSearchSummaryBatchReturnsDeletedInPlace(t *testing.T) {
	stores := newSummaryStores(t)
	kinds := putSummaryKinds(t, stores)
	entry := putSummaryNode(t, stores, kinds, kinds.entry)
	container := putSummaryNode(t, stores, kinds, kinds.container, entry)
	firstFound := putSummaryNode(t, stores, kinds, kinds.leaf, container)
	deleted := putSummaryNode(t, stores, kinds, kinds.leaf, container)
	lastFound := putSummaryNode(t, stores, kinds, kinds.leaf, container)
	if err := stores.Nodes.Delete(t.Context(), kinds.orgID, deleted); err != nil {
		t.Fatalf("delete node %s: %v", deleted, err)
	}
	requested := []uuid.UUID{firstFound, deleted, lastFound}
	wantStatus := []node.SummaryStatus{node.SummaryFound, node.SummaryDeleted, node.SummaryFound}

	transactionsBefore := fdbTransactionCount(t)
	results, err := stores.NodeSummaries(stores.SearchPolicySet()).Summaries(t.Context(), requested, summaryNameBytes)
	if err != nil {
		t.Fatalf("read %d summaries: %v", len(requested), err)
	}
	if transactions := fdbTransactionCount(t) - transactionsBefore; transactions != 1 {
		t.Fatalf("summaries of %d nodes used %d FoundationDB transactions, want 1", len(requested), transactions)
	}
	if len(results) != len(requested) {
		t.Fatalf("summaries returned %d results for %d IDs", len(results), len(requested))
	}
	for position, result := range results {
		if result.NodeID != requested[position] || result.Status != wantStatus[position] {
			t.Fatalf("result %d is node %s with status %q, want node %s with status %q",
				position, result.NodeID, result.Status, requested[position], wantStatus[position])
		}
		if result.Status == node.SummaryDeleted && (len(result.AccessKeys) != 0 || result.Summary.ID != deleted) {
			t.Fatalf("deleted node %s returned summary %s and keys %v, want its own ID and no keys", deleted, result.Summary.ID, result.AccessKeys)
		}
		if result.Status == node.SummaryFound && len(result.AccessKeys) == 0 {
			t.Fatalf("found node %s returned no access keys", result.NodeID)
		}
	}
}
