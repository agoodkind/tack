package integration_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// inheritedGrantText is the text that each inherited grant corpus node
	// repeats across several pages.
	inheritedGrantText = "inherited grant lantern text "
	// inheritedGrantRepeats sets 12 text repetitions per node to produce
	// several 128 byte pages.
	inheritedGrantRepeats = 12
	// inheritedGrantChildren sets the number of child nodes under the moved
	// parent to two.
	inheritedGrantChildren = 2
)

// TestSearchInheritedGrantUpdatesAccessOnlyWithoutModel verifies that a
// parent move changes only access and search_generation on every parent and
// child page after the test undeploys the pinned model. Each page keeps its
// document ID and stores the parent's new keys.
func TestSearchInheritedGrantUpdatesAccessOnlyWithoutModel(t *testing.T) {
	fixture := newRolloutFixture(t)
	first, second := sameOrganizationWorkspaces(t, fixture)
	parentKind := putRolloutKind(t, fixture, first.OrgID)
	childKind := putChildRolloutKind(t, fixture, first.OrgID, parentKind.typeKey)
	text := strings.Repeat(inheritedGrantText, inheritedGrantRepeats)
	parent := putRolloutNode(t, fixture, parentKind, rolloutEntryPoint(t, fixture, first), "Inherited grant parent", text)
	children := putRolloutCorpus(t, fixture, childKind, parent, text, inheritedGrantChildren)
	drainRolloutWork(t, fixture)
	affected := append([]uuid.UUID{parent}, children...)
	before := indexedPages(t, fixture, affected)
	if len(before) <= len(affected) {
		t.Fatalf("the %d affected nodes have %d pages, and each node needs several pages", len(affected), len(before))
	}
	model, err := fixture.adapter.Provision(rolloutContext(t))
	if err != nil {
		t.Fatalf("cannot read the pinned model: %v", err)
	}
	integration.UndeployNativeModel(t, fixture.adapter, fixture.client, model.ID)

	moveRolloutNode(t, fixture, parent, rolloutEntryPoint(t, fixture, second))
	drainRolloutWork(t, fixture)
	after := indexedPages(t, fixture, affected)
	requireAccessOnlyChange(t, before, after)
	parentKeys := stableIndexKeys(t, fixture, first.OrgID, parent)
	for _, page := range after {
		if keys := pageAccessOf(t, page).Keys; !slices.Equal(keys, parentKeys) {
			t.Fatalf("page %s of node %s stores keys %v instead of the parent's keys %v", page.id, page.nodeID, keys, parentKeys)
		}
	}
	for _, child := range children {
		if keys := stableIndexKeys(t, fixture, first.OrgID, child); !slices.Equal(keys, parentKeys) {
			t.Fatalf("child %s compiles keys %v instead of the parent's keys %v", child, keys, parentKeys)
		}
	}
}

// putChildRolloutKind stores a property definition and a node type that
// permits only parentType in CanLiveUnder.
func putChildRolloutKind(t *testing.T, fixture rolloutFixture, orgID uuid.UUID, parentType string) rolloutKind {
	t.Helper()
	kind := rolloutKind{orgID: orgID, typeKey: rolloutKey("n"), includedKey: rolloutKey("p")}
	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: kind.includedKey, Type: node.PropertyTypeText,
		Search: &node.SearchProjection{Include: true, Order: 0, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	if err := fixture.stores.PropertyDefs.Set(rolloutContext(t), definition); err != nil {
		t.Fatalf("cannot store property definition: %v", err)
	}
	nodeType := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: kind.typeKey, TypeKey: kind.typeKey,
		Slug: kind.typeKey, PluralSlug: kind.typeKey + "s", PropertyDefIDs: []uuid.UUID{definition.ID},
		CanLiveUnder: []string{parentType},
	}
	if err := fixture.stores.NodeTypes.Set(rolloutContext(t), nodeType); err != nil {
		t.Fatalf("cannot store node type: %v", err)
	}
	return kind
}

// stableIndexKeys compiles a node's current keys under the stable policy
// version.
func stableIndexKeys(t *testing.T, fixture rolloutFixture, orgID, nodeID uuid.UUID) []string {
	t.Helper()
	access, err := fixture.stores.SearchPolicySet().Index(rolloutContext(t), searchaccess.IndexAccessRequest{
		Version: searchaccess.StableVersion, OrganizationID: orgID, ResourceID: nodeID, Generation: 0,
	})
	if err != nil {
		t.Fatalf("cannot compile the access of node %s: %v", nodeID, err)
	}
	return access.Keys
}
