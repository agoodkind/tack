package integration

import (
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	rescanPageBytes = 256
	rescanNodeCount = 120
	rescanSlices    = 3000
)

// putUnusedDefinition stores one included property definition that no node
// uses. The write changes the organization's projection digest.
func putUnusedDefinition(t *testing.T, stores *fdbadapter.Stores, orgID uuid.UUID) {
	t.Helper()
	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: opaqueSearchKey("p"), Type: node.PropertyType(opaqueSearchKey("t")),
		Search: &node.SearchProjection{Include: true, Order: 2, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	if err := stores.PropertyDefs.Set(t.Context(), definition); err != nil {
		t.Fatalf("store unused property definition: %v", err)
	}
}

// includeStoredDefinition changes the stored definition of name to include
// its values in search text. Every fixture node stores a value under name,
// and the change alters the emitted text of every node.
func includeStoredDefinition(t *testing.T, stores *fdbadapter.Stores, orgID uuid.UUID, name string) {
	t.Helper()
	definitions, err := stores.PropertyDefs.List(t.Context(), orgID)
	if err != nil {
		t.Fatalf("list property definitions: %v", err)
	}
	for _, definition := range definitions {
		if definition.Name != name {
			continue
		}
		definition.Search.Include = true
		if err := stores.PropertyDefs.Set(t.Context(), definition); err != nil {
			t.Fatalf("include property definition %s: %v", name, err)
		}
		return
	}
	t.Fatalf("organization %s has no property definition %s", orgID, name)
}

// indexedRevisions returns the revision of the first active page of each node.
func indexedRevisions(t *testing.T, client *opensearchapi.Client, index string, nodes []uuid.UUID) map[uuid.UUID]int64 {
	t.Helper()
	revisions := make(map[uuid.UUID]int64, len(nodes))
	for _, nodeID := range nodes {
		pages := searchNodePages(t, client, index, nodeID, false)
		if len(pages) == 0 {
			t.Fatalf("node %s has no active pages", nodeID)
		}
		revision, err := strconv.ParseInt(pages[0].NodeRevision, 10, 64)
		if err != nil {
			t.Fatalf("parse revision of node %s: %v", nodeID, err)
		}
		revisions[nodeID] = revision
	}
	return revisions
}

// TestSearchRescanKeepsCursorAndSeparatesTypeChanges requires a node type
// change to leave every indexed revision unchanged. A projection change
// during a rescan must keep the scan cursor, and the finished scan must
// reindex every node, including the nodes it read before the change. A new
// definition that no node uses starts the scan. The projection change
// includes a property that every node stores.
func TestSearchRescanKeepsCursorAndSeparatesTypeChanges(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	source := clock.Wall{}
	first := putSearchText(t, stores, "rescan text", readerExcludedValue)
	nodes := []uuid.UUID{first.NodeID}
	for range rescanNodeCount {
		next := first
		next.NodeID = uuid.Must(uuid.NewV7())
		writeSearchNode(t, stores, next, "rescan text", readerExcludedValue)
		nodes = append(nodes, next.NodeID)
	}
	worker := newSearchWorker(t, stores, adapter, source, searchWorkerSettings(rescanPageBytes))
	runSearchWorkerWithin(t, worker, rescanSlices)
	initial := indexedRevisions(t, client, index, nodes)

	kind, err := stores.NodeTypes.TypeByKey(t.Context(), first.OrgID, first.TypeKey)
	if err != nil || kind == nil {
		t.Fatalf("read fixture node type: %v", err)
	}
	kind.Features = append(kind.Features, opaqueSearchKey("f"))
	if err := stores.NodeTypes.Set(t.Context(), kind); err != nil {
		t.Fatalf("change fixture node type: %v", err)
	}
	runSearchWorkerWithin(t, worker, rescanSlices)
	for nodeID, revision := range indexedRevisions(t, client, index, nodes) {
		if revision != initial[nodeID] {
			t.Fatalf("node type change reindexed node %s from revision %d to %d", nodeID, initial[nodeID], revision)
		}
	}

	putUnusedDefinition(t, stores, first.OrgID)
	store := stores.SearchWork(source)
	position := ""
	for range 20 {
		work, err := store.Claim(t.Context(), searchdomain.WorkClassRescan, "rescan-driver", time.Minute)
		if err != nil {
			t.Fatalf("claim rescan work: %v", err)
		}
		if work.Phase == searchdomain.PhasePages && work.Cursor != "" {
			position = work.Cursor
			if err := store.Yield(t.Context(), work); err != nil {
				t.Fatalf("yield rescan work: %v", err)
			}
			break
		}
		if err := worker.Process(t.Context(), work); err != nil {
			t.Fatalf("process rescan work: %v", err)
		}
	}
	if position == "" {
		t.Fatal("the rescan never checkpointed a node cursor")
	}
	includeStoredDefinition(t, stores, first.OrgID, first.ExcludedKey)
	resumed, err := store.Claim(t.Context(), searchdomain.WorkClassRescan, "rescan-driver", time.Minute)
	if err != nil || resumed.Cursor != position {
		t.Fatalf("rescan after a second request = %+v err %v, want the kept cursor %q", resumed, err, position)
	}
	if err := store.Yield(t.Context(), resumed); err != nil {
		t.Fatalf("yield resumed rescan work: %v", err)
	}
	runSearchWorkerWithin(t, worker, rescanSlices)
	for nodeID, revision := range indexedRevisions(t, client, index, nodes) {
		if revision <= initial[nodeID] {
			t.Fatalf("node %s kept revision %d after the projection change", nodeID, revision)
		}
	}
}
