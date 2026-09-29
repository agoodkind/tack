package integration

import (
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const fairnessPageBytes = 256

func linkSearchNodes(t *testing.T, stores *fdbadapter.Stores, orgID, sourceID, targetID uuid.UUID) {
	t.Helper()
	relationship := &node.Relationship{
		OrgID: orgID, SourceID: sourceID, RelationType: opaqueSearchKey("r"), TargetID: targetID,
		CreatedBy: uuid.Nil, CreatedAt: clock.Now().UTC(), Props: nil,
	}
	if err := stores.Relationships.Add(t.Context(), relationship); err != nil {
		t.Fatalf("add relationship: %v", err)
	}
}

// TestSearchAccessChangeKeepsPendingContent requires an access change to keep
// the pending content work of each affected node. The worker must then index
// every page of both nodes at generation 2 or later.
func TestSearchAccessChangeKeepsPendingContent(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	source := putSearchText(t, stores, "pending content", readerExcludedValue)
	target := putSearchTextInOrg(t, stores, source.OrgID, "target content", readerExcludedValue)
	linkSearchNodes(t, stores, source.OrgID, source.NodeID, target.NodeID)

	store := stores.SearchWork(clock.Wall{})
	pending := claimAll(t, store, searchdomain.WorkClassLive)
	for _, nodeID := range []uuid.UUID{source.NodeID, target.NodeID} {
		work, exists := pending[nodeID]
		if !exists || work.Generation != 2 || work.Revision != "1" {
			t.Fatalf("live work of %s after the access change = %+v, want revision 1 at generation 2", nodeID, work)
		}
		if err := store.Yield(t.Context(), work); err != nil {
			t.Fatalf("yield inspected work: %v", err)
		}
	}
	runSearchWorkerUntilIdle(t, newSearchWorker(t, stores, adapter, clock.Wall{}, searchWorkerSettings(fairnessPageBytes)))
	for _, fixture := range []searchFixture{source, target} {
		pages := readSearchPages(t, stores, fixture.NodeID, fairnessPageBytes)
		requireIndexedPages(t, searchNodePages(t, client, index, fixture.NodeID, false), pages, 2)
	}
}

// TestSearchClassRotationAdmitsLiveWork requires a live item to run within
// two slices while an access backlog is pending. Strict access priority
// fails this test because it drains every access item first.
func TestSearchClassRotationAdmitsLiveWork(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	first := putSearchText(t, stores, "backlog", readerExcludedValue)
	nodes := []uuid.UUID{first.NodeID}
	for range 5 {
		nodes = append(nodes, putSearchTextInOrg(t, stores, first.OrgID, "backlog", readerExcludedValue).NodeID)
	}
	worker := newSearchWorker(t, stores, adapter, clock.Wall{}, searchWorkerSettings(fairnessPageBytes))
	runSearchWorkerUntilIdle(t, worker)
	for position := 1; position < len(nodes); position++ {
		linkSearchNodes(t, stores, first.OrgID, nodes[position-1], nodes[position])
	}
	late := first
	late.NodeID = uuid.Must(uuid.NewV7())
	writeSearchNode(t, stores, late, "late live work", readerExcludedValue)
	for range 2 {
		if _, err := worker.RunSlice(t.Context()); err != nil {
			t.Fatalf("run search worker slice: %v", err)
		}
	}
	if documents := searchNodePages(t, client, index, late.NodeID, false); len(documents) == 0 {
		t.Fatal("live work waited behind the access backlog")
	}
	if len(claimAll(t, stores.SearchWork(clock.Wall{}), searchdomain.WorkClassAccess)) == 0 {
		t.Fatal("the access backlog drained before the live check")
	}
}
