package integration_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

func verifySecretSearchPageOperations(t *testing.T, runtime *search.Adapter, index string) {
	t.Helper()
	nodeID := uuid.New()
	access := node.SearchAccess{Versions: []string{"fixture-policy"}, Keys: []string{"fixture-access"}, Generation: 1}
	work := searchdomain.Work{
		OrgID: uuid.New(), NodeID: nodeID, Generation: 1, Revision: "1", Projection: "fixture-projection",
		Cursor: "", Ordinal: 0, Phase: searchdomain.PhasePages, Deleted: false,
		EnqueuedAt: time.Now(), Owner: "fixture", LeaseUntil: time.Now().Add(time.Minute),
		Class: searchdomain.WorkClassLive, Target: index, Mirror: "",
	}
	page := node.ContentPage{
		NodeID: nodeID, NodeType: "fixture-type", Revision: "1", ProjectionVersion: "fixture-projection",
		Name: "Synthetic service credential fixture", Text: "A synthetic service credential fixture tests storage access.",
		Access: access, Ordinal: 0, OverlapBytes: 0, NextCursor: "", Done: true,
	}
	documentID := searchdomain.DocumentID(work.OrgID, nodeID, page.Revision, page.ProjectionVersion, page.Ordinal)
	if written, err := runtime.Put(t.Context(), searchdomain.WriteIntent{Work: work, Page: page, DocumentID: documentID}); err != nil || written == 0 {
		t.Fatalf("runtime bulk page write and realtime multiget failed: %v", err)
	}
	if err := runtime.Refresh(t.Context(), index); err != nil {
		t.Fatal(err)
	}
	ranker := runtime.Ranker(search.RankerSettings{KeepAlive: time.Minute, TokenBytes: 65536, BatchSize: 100})
	query := searchdomain.Query{
		Text: "synthetic service credential fixture", Index: index, NodeType: "",
		Access: searchdomain.AccessFilter{Version: "fixture-policy", Keys: []string{"fixture-access"}}, Generation: 0,
	}
	snapshot, err := ranker.Open(t.Context(), query)
	if err != nil {
		t.Fatalf("runtime query inference and point-in-time creation failed: %v", err)
	}
	batch, err := ranker.Read(t.Context(), query, snapshot, nil)
	if err != nil || len(batch.Hits) != 1 || batch.Hits[0].NodeID != nodeID {
		t.Fatalf("runtime ranked page read failed: %v", err)
	}
	if err := ranker.Close(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	documents := []searchdomain.IssuedDocument{{DocumentID: documentID, Revision: 1, Ordinal: 0, Projection: page.ProjectionVersion}}
	work.Generation = 2
	access.Generation = 2
	if accepted, err := runtime.UpdateAccess(t.Context(), searchdomain.AccessIntent{Work: work, Documents: documents, Access: access}); err != nil || accepted != 1 {
		t.Fatalf("runtime access update failed: %v", err)
	}
	work.Generation = 3
	if accepted, err := runtime.Retire(t.Context(), searchdomain.RetirementIntent{Work: work, Documents: documents}); err != nil || accepted != 1 {
		t.Fatalf("runtime retirement failed: %v", err)
	}
	if err := runtime.Refresh(t.Context(), index); err != nil {
		t.Fatal(err)
	}
	retention, err := runtime.IndexRetention(t.Context(), index)
	if err != nil || retention.RetiredPages != 1 {
		t.Fatalf("runtime did not count its retired page: %v", err)
	}
}
