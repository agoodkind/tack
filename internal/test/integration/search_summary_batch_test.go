package integration

import (
	"errors"
	"expvar"
	"slices"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/testenv"
)

// summaryBatchLeaves is the number of valid leaves in the summary batch. The
// batch adds one container and three defective leaves for 100 IDs, the
// summary bound of one OpenSearch batch.
const summaryBatchLeaves = 96

// summaryNameBytes bounds summary names in the batch test.
const summaryNameBytes = 256

// summaryHierarchyKinds is an entry-point type, a container type that lives
// under it, and a leaf type that lives under the container.
type summaryHierarchyKinds struct {
	orgID                  uuid.UUID
	entry, container, leaf string
}

// TestSearchSummaryBatchReadsOnce reads the summaries of 100 nodes across two
// entry points and requires one FoundationDB transaction for the whole batch.
// Each valid node has the keys that indexing compiles for it. Each node with
// a hierarchy defect is found with no keys.
func TestSearchSummaryBatchReadsOnce(t *testing.T) {
	stores := newSummaryStores(t)
	kinds := putSummaryKinds(t, stores)
	entries := []uuid.UUID{putSummaryNode(t, stores, kinds, kinds.entry), putSummaryNode(t, stores, kinds, kinds.entry)}
	containers := []uuid.UUID{
		putSummaryNode(t, stores, kinds, kinds.container, entries[0]),
		putSummaryNode(t, stores, kinds, kinds.container, entries[1]),
	}
	requested := []uuid.UUID{containers[0]}
	entryOf := map[uuid.UUID]uuid.UUID{containers[0]: entries[0]}
	for i := range summaryBatchLeaves {
		leafID := putSummaryNode(t, stores, kinds, kinds.leaf, containers[i%2])
		requested = append(requested, leafID)
		entryOf[leafID] = entries[i%2]
	}
	orphan := putSummaryNode(t, stores, kinds, kinds.leaf)
	twoParents := putSummaryNode(t, stores, kinds, kinds.leaf, containers[0], containers[1])
	missingParent := putSummaryNode(t, stores, kinds, kinds.leaf, uuid.Must(uuid.NewV7()))
	requested = append(requested, orphan, twoParents, missingParent)

	policies := stores.SearchPolicySet()
	before := fdbTransactionCount(t)
	results, err := stores.NodeSummaries(policies).Summaries(t.Context(), requested, summaryNameBytes)
	if err != nil {
		t.Fatalf("read %d summaries: %v", len(requested), err)
	}
	if transactions := fdbTransactionCount(t) - before; transactions != 1 {
		t.Fatalf("summaries of %d nodes used %d FoundationDB transactions, want 1", len(requested), transactions)
	}
	if len(results) != len(requested) {
		t.Fatalf("summaries returned %d results for %d IDs", len(results), len(requested))
	}
	for position, result := range results {
		nodeID := requested[position]
		if result.NodeID != nodeID || result.Status != node.SummaryFound {
			t.Fatalf("result %d is node %s with status %q, want found node %s", position, result.NodeID, result.Status, nodeID)
		}
		if entryPointID, valid := entryOf[nodeID]; valid {
			requireIndexedKeys(t, policies, kinds.orgID, nodeID, entryPointID, result.AccessKeys)
			continue
		}
		if len(result.AccessKeys) != 0 {
			t.Errorf("node %s has a hierarchy defect and received keys %v", nodeID, result.AccessKeys)
		}
	}
	_, err = policies.Index(t.Context(), searchaccess.IndexAccessRequest{
		Version: searchaccess.StableVersion, OrganizationID: kinds.orgID, ResourceID: orphan, Generation: 0,
	})
	if !errors.Is(err, searchaccess.ErrNoHierarchyParent) {
		t.Fatalf("indexing access of orphan %s returned %v, want ErrNoHierarchyParent", orphan, err)
	}
}

// requireIndexedKeys requires keys to equal the keys that indexing compiles
// for nodeID under both policy versions. It also requires the indexed keys of
// nodeID to equal the indexed keys of its entry point.
func requireIndexedKeys(t *testing.T, policies *searchaccess.PolicySet, orgID, nodeID, entryPointID uuid.UUID, keys []string) {
	t.Helper()
	want := []string{}
	for _, version := range []string{searchaccess.StableVersion, searchaccess.RotatedVersion} {
		access := indexAccess(t, policies, version, orgID, nodeID)
		entryAccess := indexAccess(t, policies, version, orgID, entryPointID)
		if !slices.Equal(access.Keys, entryAccess.Keys) {
			t.Fatalf("node %s has keys %v under %s, and its entry point %s has %v", nodeID, access.Keys, version, entryPointID, entryAccess.Keys)
		}
		want = append(want, access.Keys...)
	}
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Errorf("summary of node %s has keys %v, want indexed keys %v", nodeID, keys, want)
	}
}

func indexAccess(t *testing.T, policies *searchaccess.PolicySet, version string, orgID, nodeID uuid.UUID) node.SearchAccess {
	t.Helper()
	access, err := policies.Index(t.Context(), searchaccess.IndexAccessRequest{
		Version: version, OrganizationID: orgID, ResourceID: nodeID, Generation: 0,
	})
	if err != nil {
		t.Fatalf("index access of node %s under %s: %v", nodeID, version, err)
	}
	return access
}

// fdbTransactionCount reads the process counter that every completed
// FoundationDB store operation increments.
func fdbTransactionCount(t *testing.T) int64 {
	t.Helper()
	counter, isInt := expvar.Get("tack_fdb_tx_total").(*expvar.Int)
	if !isInt {
		t.Fatal("expvar tack_fdb_tx_total is not registered as an integer")
	}
	return counter.Value()
}

// newSummaryStores opens stores under an isolated test prefix. The stores
// schedule no search work. No worker transaction runs during the test.
func newSummaryStores(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	cluster := testenv.FoundationDB(t)
	prefix := append([]byte("search-summary-test:"), uuid.Must(uuid.NewV7()).String()...)
	prefix = append(prefix, ':')
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() {
		clearPrefix(t, cluster, prefix)
		fdbadapter.SetTestPrefix(nil)
	})
	stores, err := fdbadapter.NewStores(cluster, testTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open summary stores: %v", err)
	}
	return stores
}

// putSummaryKinds stores the three opaque node types of one new organization.
func putSummaryKinds(t *testing.T, stores *fdbadapter.Stores) summaryHierarchyKinds {
	t.Helper()
	kinds := summaryHierarchyKinds{
		orgID: uuid.Must(uuid.NewV7()), entry: opaqueSearchKey("e"),
		container: opaqueSearchKey("c"), leaf: opaqueSearchKey("l"),
	}
	types := []*node.NodeType{
		{TypeKey: kinds.entry, Features: node.Features{node.FeatureIsEntryPoint}},
		{TypeKey: kinds.container, CanLiveUnder: []string{kinds.entry}},
		{TypeKey: kinds.leaf, CanLiveUnder: []string{kinds.container}},
	}
	for _, kind := range types {
		kind.ID, kind.OrgID, kind.Name = uuid.Must(uuid.NewV7()), kinds.orgID, kind.TypeKey
		kind.Slug, kind.PluralSlug = kind.TypeKey, kind.TypeKey+"s"
		if err := stores.NodeTypes.Set(t.Context(), kind); err != nil {
			t.Fatalf("store node type %s: %v", kind.TypeKey, err)
		}
	}
	return kinds
}

// putSummaryNode creates one node of typeKey with one child_of edge to each
// parent in the same transaction.
func putSummaryNode(t *testing.T, stores *fdbadapter.Stores, kinds summaryHierarchyKinds, typeKey string, parents ...uuid.UUID) uuid.UUID {
	t.Helper()
	nodeID := uuid.Must(uuid.NewV7())
	now := clock.Now().UTC()
	created := &node.Node{ID: nodeID, OrgID: kinds.orgID, NodeType: typeKey, Name: "summary " + typeKey, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: nodeID, OrgID: kinds.orgID, NodeType: typeKey, Name: created.Name, CreatedAt: now, UpdatedAt: now}
	relationships := make([]*node.Relationship, 0, len(parents))
	for _, parentID := range parents {
		relationships = append(relationships, &node.Relationship{
			OrgID: kinds.orgID, SourceID: nodeID, RelationType: node.RelChildOf, TargetID: parentID, CreatedAt: now,
		})
	}
	if err := stores.Nodes.CreateAtomic(t.Context(), created, view, relationships, nil, nil, nil); err != nil {
		t.Fatalf("create %s node: %v", typeKey, err)
	}
	return nodeID
}
