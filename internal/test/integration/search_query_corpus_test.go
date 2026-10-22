package integration

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// opaqueKind represents a generated node type with one included and one
// excluded text property. The type key and both property names use
// UUID-derived identifiers without product meaning.
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
			ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: kind.IncludedKey, Type: node.PropertyTypeText,
			Search: &node.SearchProjection{Include: true, Order: 0, Rule: node.TextRule{Mode: node.TextRuleScalar}},
		},
		{
			ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: kind.ExcludedKey, Type: node.PropertyTypeText,
			Search: &node.SearchProjection{Include: false, Order: 1, Rule: node.TextRule{Mode: node.TextRuleScalar}},
		},
	}
	for _, definition := range definitions {
		if err := fixture.Stores.PropertyDefs.Set(WithTestOperation(t, t.Context()), definition); err != nil {
			t.Fatalf("store property definition: %v", err)
		}
	}
	nodeType := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: kind.TypeKey, TypeKey: kind.TypeKey,
		Slug: kind.TypeKey, PluralSlug: kind.TypeKey + "s",
		PropertyDefIDs: []uuid.UUID{definitions[0].ID, definitions[1].ID},
		CanLiveUnder:   entryTypeKeys(t, fixture, orgID),
	}
	if err := fixture.Stores.NodeTypes.Set(WithTestOperation(t, t.Context()), nodeType); err != nil {
		t.Fatalf("store node type: %v", err)
	}
	return kind
}

// entryTypeKeys returns the type keys of the entry-point node types in orgID.
// An opaque type declares these keys as its parents, and the access
// hierarchy then depends only on NodeType metadata.
func entryTypeKeys(t *testing.T, fixture queryFixture, orgID uuid.UUID) []string {
	t.Helper()
	types, err := fixture.Stores.NodeTypes.List(WithTestOperation(t, t.Context()), orgID)
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

// fixtureNodeService builds the production node service over the
// fixture's FoundationDB stores.
func fixtureNodeService(fixture queryFixture) *service.NodeService {
	stores := fixture.Stores
	return service.NewNodeService(stores.Nodes, stores.Views, stores.NodeTypes, stores.PropertyDefs, stores.Relationships, stores.NodeDeleter)
}

// putOpaqueNode creates a node of kind under parentID through
// NodeService.Create and returns its ID. The service writes parent_id,
// scope_id, the child_of edge, and search work in one FoundationDB
// transaction.
func putOpaqueNode(t *testing.T, fixture queryFixture, kind opaqueKind, parentID uuid.UUID, name, included, excluded string) uuid.UUID {
	t.Helper()
	props := map[string]json.RawMessage{kind.IncludedKey: mustJSON(included), kind.ExcludedKey: mustJSON(excluded)}
	result, err := fixtureNodeService(fixture).Create(WithTestOperation(t, t.Context()), service.CreateInput{
		ParentID: parentID, NodeTypeKey: kind.TypeKey, Name: name, Props: props,
	})
	if err != nil {
		t.Fatalf("create opaque node of type %s under %s: %v", kind.TypeKey, parentID, err)
	}
	return result.View.ID
}

// moveOpaqueNode reads nodeID and fails the test unless its type matches
// kind.TypeKey and its parent_id equals from. It calls NodeService.Update
// with both parent_id and scope_id set to to. Every caller passes a workspace
// entry node as to. The update replaces the child_of edge in the same
// transaction.
func moveOpaqueNode(t *testing.T, fixture queryFixture, kind opaqueKind, nodeID, from, to uuid.UUID) {
	t.Helper()
	current, err := fixture.Stores.Views.Get(WithTestOperation(t, t.Context()), nodeID)
	if err != nil || current == nil {
		t.Fatalf("read node %s before the move: %v", nodeID, err)
	}
	if current.NodeType != kind.TypeKey || !bytes.Equal(current.Props["parent_id"], mustJSON(from.String())) {
		t.Fatalf("node %s has type %s and parent_id %s, want type %s under %s", nodeID, current.NodeType, current.Props["parent_id"], kind.TypeKey, from)
	}
	if _, err := fixtureNodeService(fixture).Update(WithTestOperation(t, t.Context()), service.UpdateInput{
		NodeID: nodeID, Props: map[string]json.RawMessage{"parent_id": mustJSON(to.String()), "scope_id": mustJSON(to.String())},
	}); err != nil {
		t.Fatalf("move node %s from %s to %s: %v", nodeID, from, to, err)
	}
}

// drainSearchWork runs the production worker until no work is claimable.
func drainSearchWork(t *testing.T, worker *service.SearchWorker, maxSlices int) {
	t.Helper()
	runSearchWorkerWithin(t, worker, maxSlices)
}
