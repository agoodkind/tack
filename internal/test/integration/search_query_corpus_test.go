package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// opaqueKind is one generated node type with one included and one excluded
// property of unfamiliar opaque types.
type opaqueKind struct {
	OrgID       uuid.UUID
	TypeKey     string
	IncludedKey string
	ExcludedKey string
}

// putOpaqueKind stores an opaque child type in orgID through the production
// metadata stores. Every property declares search inclusion explicitly.
func putOpaqueKind(t *testing.T, fixture queryFixture, orgID uuid.UUID) opaqueKind {
	t.Helper()
	return putOpaqueKindWithTypeKey(t, fixture, orgID, opaqueSearchKey("n"))
}

// putOpaqueKindWithTypeKey stores the same opaque child type as
// putOpaqueKind under the given type key.
func putOpaqueKindWithTypeKey(t *testing.T, fixture queryFixture, orgID uuid.UUID, typeKey string) opaqueKind {
	t.Helper()
	kind := opaqueKind{OrgID: orgID, TypeKey: typeKey, IncludedKey: opaqueSearchKey("p"), ExcludedKey: opaqueSearchKey("p")}
	definitions := []*node.PropertyDef{
		{
			ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: kind.IncludedKey, Type: node.PropertyType(opaqueSearchKey("t")),
			Search: &node.SearchProjection{Include: true, Order: 0, Rule: node.TextRule{Mode: node.TextRuleScalar}},
		},
		{
			ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: kind.ExcludedKey, Type: node.PropertyType(opaqueSearchKey("t")),
			Search: &node.SearchProjection{Include: false, Order: 1, Rule: node.TextRule{Mode: node.TextRuleScalar}},
		},
	}
	for _, definition := range definitions {
		if err := fixture.Stores.PropertyDefs.Set(t.Context(), definition); err != nil {
			t.Fatalf("store property definition: %v", err)
		}
	}
	nodeType := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: kind.TypeKey, TypeKey: kind.TypeKey,
		Slug: kind.TypeKey, PluralSlug: kind.TypeKey + "s",
		PropertyDefIDs: []uuid.UUID{definitions[0].ID, definitions[1].ID},
		CanLiveUnder:   entryTypeKeys(t, fixture, orgID),
	}
	if err := fixture.Stores.NodeTypes.Set(t.Context(), nodeType); err != nil {
		t.Fatalf("store node type: %v", err)
	}
	return kind
}

// entryTypeKeys returns the type keys of orgID's entry-point node types. An
// opaque type declares them as its parents. The access hierarchy then comes
// from NodeType metadata.
func entryTypeKeys(t *testing.T, fixture queryFixture, orgID uuid.UUID) []string {
	t.Helper()
	types, err := fixture.Stores.NodeTypes.List(t.Context(), orgID)
	if err != nil {
		t.Fatalf("list node types: %v", err)
	}
	keys := make([]string, 0, 1)
	for _, kind := range types {
		if kind.Features.Has(node.FeatureIsEntryPoint) {
			keys = append(keys, kind.TypeKey)
		}
	}
	if len(keys) == 0 {
		t.Fatalf("organization %s has no entry-point type", orgID)
	}
	return keys
}

// putOpaqueNode creates one node of kind under parentID through the
// production create path, which records durable search work.
func putOpaqueNode(t *testing.T, fixture queryFixture, kind opaqueKind, parentID uuid.UUID, name, included, excluded string) uuid.UUID {
	t.Helper()
	nodeID := uuid.Must(uuid.NewV7())
	props := map[string]json.RawMessage{kind.IncludedKey: mustJSON(included), kind.ExcludedKey: mustJSON(excluded)}
	now := clock.Now().UTC()
	created := &node.Node{ID: nodeID, OrgID: kind.OrgID, NodeType: kind.TypeKey, Name: name, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: nodeID, OrgID: kind.OrgID, NodeType: kind.TypeKey, Name: name, Props: props, CreatedAt: now, UpdatedAt: now}
	parent := &node.Relationship{OrgID: kind.OrgID, SourceID: nodeID, TargetID: parentID, RelationType: node.RelChildOf}
	if err := fixture.Stores.Nodes.CreateAtomic(t.Context(), created, view, []*node.Relationship{parent}, nil, nil, nil); err != nil {
		t.Fatalf("create opaque node: %v", err)
	}
	return nodeID
}

// moveOpaqueNode replaces the node's parent relationship.
func moveOpaqueNode(t *testing.T, fixture queryFixture, kind opaqueKind, nodeID, from, to uuid.UUID) {
	t.Helper()
	if err := fixture.Stores.Relationships.Remove(t.Context(), kind.OrgID, nodeID, node.RelChildOf, from); err != nil {
		t.Fatalf("remove parent of node %s: %v", nodeID, err)
	}
	moved := &node.Relationship{OrgID: kind.OrgID, SourceID: nodeID, TargetID: to, RelationType: node.RelChildOf}
	if err := fixture.Stores.Relationships.Add(t.Context(), moved); err != nil {
		t.Fatalf("add parent of node %s: %v", nodeID, err)
	}
}

// drainSearchWork runs the production worker until no work is claimable.
func drainSearchWork(t *testing.T, worker *service.SearchWorker, maxSlices int) {
	t.Helper()
	runSearchWorkerWithin(t, worker, maxSlices)
}
