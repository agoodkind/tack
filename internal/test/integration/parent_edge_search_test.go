package integration_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// parentChainLength is the number of leaves in the moved chain.
const parentChainLength = 40

// TestSearchParentEdgeLeafUnderLeaf creates a leaf under the first
// entry point and a second leaf under the first leaf through the node
// service. No metadata declares the pair of the two leaf types. Search
// under the first entry point must return the second leaf, and its summary
// keys must equal the summary keys of the entry point.
func TestSearchParentEdgeLeafUnderLeaf(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	parent := fixture.createLeaf(t, fixture.firstEntry, "cinder parent leaf")
	nested := fixture.createLeaf(t, parent, "cinder nested leaf")
	fixture.drain(t)

	if found := fixture.search(t, fixture.first, "cinder nested leaf"); !slices.Contains(found, nested) {
		t.Fatalf("search under the first entry point returned %v, want nested leaf %s", found, nested)
	}
	keys := fixture.summaryKeys(t, nested, parent, fixture.firstEntry)
	if len(keys[nested]) == 0 || !slices.Equal(keys[nested], keys[fixture.firstEntry]) || !slices.Equal(keys[parent], keys[fixture.firstEntry]) {
		t.Fatalf("summary keys: nested leaf %v, parent leaf %v, entry point %v; want three equal sets", keys[nested], keys[parent], keys[fixture.firstEntry])
	}
}

// TestSearchParentEdgeMovedChain creates a chain of 40 leaves
// under the first entry point, each leaf under the one before it, and moves
// the top leaf to the second entry point through the node service. Every
// leaf of the chain must then have the summary keys of the second entry
// point. Search under the first entry point must return none of the leaves,
// and search under the second entry point must return all of them.
func TestSearchParentEdgeMovedChain(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	chain := make([]uuid.UUID, 0, parentChainLength)
	parent := fixture.firstEntry
	for range parentChainLength {
		parent = fixture.createLeaf(t, parent, "basalt chain leaf")
		chain = append(chain, parent)
	}
	fixture.drain(t)
	if found := intersect(fixture.search(t, fixture.first, "basalt chain leaf"), chain); len(found) != parentChainLength {
		t.Fatalf("search under the first entry point returned %d chain leaves before the move, want %d", len(found), parentChainLength)
	}

	if err := fixture.moveLeaf(t, chain[0], fixture.secondEntry); err != nil {
		t.Fatalf("move top leaf %s to the second entry point: %v", chain[0], err)
	}
	fixture.drain(t)

	keys := fixture.summaryKeys(t, append([]uuid.UUID{fixture.secondEntry}, chain...)...)
	for _, leaf := range chain {
		if len(keys[leaf]) == 0 || !slices.Equal(keys[leaf], keys[fixture.secondEntry]) {
			t.Fatalf("leaf %s has summary keys %v after the move, want the second entry point keys %v", leaf, keys[leaf], keys[fixture.secondEntry])
		}
	}
	fixture.eventually(t, func() string {
		if found := intersect(fixture.search(t, fixture.first, "basalt chain leaf"), chain); len(found) != 0 {
			return fmt.Sprintf("search under the first entry point returned moved leaves %v", found)
		}
		if found := intersect(fixture.search(t, fixture.second, "basalt chain leaf"), chain); len(found) != parentChainLength {
			return fmt.Sprintf("search under the second entry point returned %d moved leaves, want %d", len(found), parentChainLength)
		}
		return ""
	})
}

// TestSearchParentEdgeMembershipEdge stores a node of a type without
// CanLiveUnder under the first entry point and indexes it. The test then
// replaces the child_of edge with a member_of edge to the same entry point.
// It runs no search work after the change, and the indexed pages keep the
// keys of the entry point. Only child_of edges create access. The summary
// keys of the node must not include a key of the entry point, and the final
// FoundationDB check of tack_search must withhold the node.
func TestSearchParentEdgeMembershipEdge(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	rootKind := fixture.putKind(t, fixture.kind.orgID, nil)
	member := fixture.storeNode(t, rootKind, "garnet member node",
		&node.Relationship{RelationType: node.RelChildOf, TargetID: fixture.firstEntry})
	fixture.drain(t)
	if found := fixture.search(t, fixture.first, "garnet member node"); !slices.Contains(found, member) {
		t.Fatalf("search returned %v before the edge change, want node %s", found, member)
	}

	relationships := fixture.stores.Relationships
	membership := &node.Relationship{OrgID: rootKind.orgID, SourceID: member, RelationType: "member_of", TargetID: fixture.firstEntry}
	if err := relationships.Add(t.Context(), membership); err != nil {
		t.Fatalf("add member_of edge of node %s: %v", member, err)
	}
	if err := relationships.Remove(t.Context(), rootKind.orgID, member, node.RelChildOf, fixture.firstEntry); err != nil {
		t.Fatalf("remove child_of edge of node %s: %v", member, err)
	}

	keys := fixture.summaryKeys(t, member, fixture.firstEntry)
	if shared := intersect(keys[member], keys[fixture.firstEntry]); len(shared) != 0 {
		t.Fatalf("node %s with a member_of edge has the entry point keys %v", member, shared)
	}
	if found := fixture.search(t, fixture.first, "garnet member node"); slices.Contains(found, member) {
		t.Fatalf("search returned node %s, which has no child_of edge to the entry point", member)
	}
}
