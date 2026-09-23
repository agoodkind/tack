package integration

import (
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/searchaccess"
)

func TestSearchScopeAccessUsesEntryPoint(t *testing.T) {
	stores := newSearchStore(t)
	ctx := t.Context()
	orgID := uuid.Must(uuid.NewV7())
	for _, definition := range []*node.NodeType{
		{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, TypeKey: "search_entry", Features: node.Features{node.FeatureIsEntryPoint}},
		{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, TypeKey: "search_child", CanLiveUnder: []string{"search_entry"}},
	} {
		if err := stores.NodeTypes.Set(ctx, definition); err != nil {
			t.Fatalf("store search node type %s: %v", definition.TypeKey, err)
		}
	}
	firstEntry := createScopeAccessNode(t, stores, orgID, "search_entry")
	secondEntry := createScopeAccessNode(t, stores, orgID, "search_entry")
	firstChild := createScopeAccessNode(t, stores, orgID, "search_child", firstEntry)
	secondChild := createScopeAccessNode(t, stores, orgID, "search_child", firstEntry)
	otherChild := createScopeAccessNode(t, stores, orgID, "search_child", secondEntry)

	policies := stores.SearchPolicySet()
	compile := func(resourceID uuid.UUID) (node.SearchAccess, error) {
		return policies.Index(ctx, searchaccess.IndexAccessRequest{
			Version: searchaccess.StableVersion, OrganizationID: orgID, ResourceID: resourceID, Generation: 1,
		})
	}
	first, err := compile(firstChild)
	if err != nil {
		t.Fatalf("read first child access: %v", err)
	}
	second, err := compile(secondChild)
	if err != nil {
		t.Fatalf("read second child access: %v", err)
	}
	other, err := compile(otherChild)
	if err != nil {
		t.Fatalf("read other entry point access: %v", err)
	}
	if len(first.Keys) != 1 || len(second.Keys) != 1 || len(other.Keys) != 1 {
		t.Fatalf("expected one opaque key per node: %v, %v, %v", first.Keys, second.Keys, other.Keys)
	}
	if first.Keys[0] != second.Keys[0] {
		t.Fatalf("descendants under one entry point have different access keys")
	}
	if first.Keys[0] == other.Keys[0] {
		t.Fatalf("different entry points have the same access key")
	}

	orphan := createScopeAccessNode(t, stores, orgID, "search_child")
	if _, err := compile(orphan); err == nil {
		t.Fatalf("node without an entry-point parent received search access")
	}
	if err := stores.Relationships.Add(ctx, &node.Relationship{
		OrgID: orgID, SourceID: firstChild, TargetID: secondEntry, RelationType: node.RelChildOf,
	}); err != nil {
		t.Fatalf("add second parent: %v", err)
	}
	if _, err := compile(firstChild); err == nil {
		t.Fatalf("node with ambiguous parents received search access")
	}
}

func createScopeAccessNode(t *testing.T, stores *fdbadapter.Stores, orgID uuid.UUID, typeKey string, parentIDs ...uuid.UUID) uuid.UUID {
	t.Helper()
	nodeID := uuid.Must(uuid.NewV7())
	now := clock.Now().UTC()
	created := &node.Node{ID: nodeID, OrgID: orgID, NodeType: typeKey, Name: typeKey, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: nodeID, OrgID: orgID, NodeType: typeKey, Name: typeKey, CreatedAt: now, UpdatedAt: now}
	relationships := make([]*node.Relationship, 0, len(parentIDs))
	for _, parentID := range parentIDs {
		relationships = append(relationships, &node.Relationship{
			OrgID: orgID, SourceID: nodeID, TargetID: parentID, RelationType: node.RelChildOf,
		})
	}
	if err := stores.Nodes.CreateAtomic(t.Context(), created, view, relationships, nil, nil, nil); err != nil {
		t.Fatalf("create search node %s: %v", nodeID, err)
	}
	return nodeID
}
