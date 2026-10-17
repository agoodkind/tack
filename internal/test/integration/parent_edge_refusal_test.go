package integration_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/searchaccess"
)

// TestParentEdgeRefusesCrossOrganizationParent requires the node service to
// refuse a parent in another organization. A child_of edge that the node
// store writes to a node of another organization must give the node a
// hierarchy defect and no summary keys.
func TestParentEdgeRefusesCrossOrganizationParent(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	foreignEntry := fixture.entry(t, fixture.foreign)
	leaf := fixture.createLeaf(t, fixture.firstEntry, "quartz local leaf")
	err := fixture.moveLeaf(t, leaf, foreignEntry)
	if !errors.Is(err, domain.ErrInvalidArgument) || !strings.Contains(err.Error(), foreignEntry.String()) {
		t.Fatalf("move of leaf %s to entry point %s of another organization returned %v, want a refusal", leaf, foreignEntry, err)
	}

	stored := fixture.storeNode(t, fixture.kind, "quartz foreign parent",
		&node.Relationship{RelationType: node.RelChildOf, TargetID: foreignEntry})
	requireParentEdgeDefect(t, fixture, stored, searchaccess.ErrHierarchyDefect)
}

// TestParentEdgeRefusesCycle creates a leaf under the first entry point and
// a nested leaf under it. The node service must refuse the nested leaf as
// the parent of the outer leaf, and the outer leaf as its own parent. Each
// error must contain the IDs of both nodes.
func TestParentEdgeRefusesCycle(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	outer := fixture.createLeaf(t, fixture.firstEntry, "onyx outer leaf")
	nested := fixture.createLeaf(t, outer, "onyx nested leaf")
	for _, parentID := range []uuid.UUID{nested, outer} {
		err := fixture.moveLeaf(t, outer, parentID)
		if !errors.Is(err, domain.ErrInvalidArgument) || !strings.Contains(err.Error(), outer.String()) || !strings.Contains(err.Error(), parentID.String()) {
			t.Fatalf("move of leaf %s under %s returned %v, want a cycle refusal with both IDs", outer, parentID, err)
		}
	}
}

// TestParentEdgeTwoParentsAreDefect adds a second child_of edge through the
// relationship store to a leaf under the first entry point. The leaf must
// have a hierarchy defect that matches node.ErrNoHierarchyParent and no
// summary keys.
func TestParentEdgeTwoParentsAreDefect(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	leaf := fixture.createLeaf(t, fixture.firstEntry, "jasper two parent leaf")
	second := &node.Relationship{OrgID: fixture.kind.orgID, SourceID: leaf, RelationType: node.RelChildOf, TargetID: fixture.secondEntry}
	if err := fixture.stores.Relationships.Add(t.Context(), second); err != nil {
		t.Fatalf("add second parent edge of leaf %s: %v", leaf, err)
	}
	requireParentEdgeDefect(t, fixture, leaf, node.ErrNoHierarchyParent)
}

// TestParentEdgeRefusesOnlyParentRemoval creates a leaf under another leaf.
// No metadata declares that pair of types. The relationship store must
// refuse the removal of the only child_of edge of the nested leaf and keep
// the edge.
func TestParentEdgeRefusesOnlyParentRemoval(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	parent := fixture.createLeaf(t, fixture.firstEntry, "amber parent leaf")
	nested := fixture.createLeaf(t, parent, "amber nested leaf")
	relationships := fixture.stores.Relationships
	err := relationships.Remove(t.Context(), fixture.kind.orgID, nested, node.RelChildOf, parent)
	if !errors.Is(err, domain.ErrFailedPrecondition) || !strings.Contains(err.Error(), "only hierarchy parent") {
		t.Fatalf("removal of the only parent edge of leaf %s returned %v, want a refusal", nested, err)
	}
	if _, err := fixture.index(t, nested); err != nil {
		t.Fatalf("index access of leaf %s after the refused removal: %v", nested, err)
	}
}

// requireParentEdgeDefect requires the index access compilation of nodeID to
// fail with an error that matches want, and the summary of nodeID to have no
// keys.
func requireParentEdgeDefect(t *testing.T, fixture parentEdgeFixture, nodeID uuid.UUID, want error) {
	t.Helper()
	if _, err := fixture.index(t, nodeID); !errors.Is(err, want) {
		t.Fatalf("index access of node %s returned %v, want %v", nodeID, err, want)
	}
	if keys := fixture.summaryKeys(t, nodeID)[nodeID]; len(keys) != 0 {
		t.Fatalf("node %s has a hierarchy defect and has summary keys %v", nodeID, keys)
	}
}
