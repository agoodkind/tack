package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// rolloutKind defines a generated node type with one searchable text
// property. Its identifiers do not have product meaning.
type rolloutKind struct {
	orgID       uuid.UUID
	typeKey     string
	includedKey string
}

func rolloutKey(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
}

// putRolloutKind stores a property definition and a node type that can
// live under the organization's entry points.
func putRolloutKind(t *testing.T, fixture rolloutFixture, orgID uuid.UUID) rolloutKind {
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
		CanLiveUnder: rolloutEntryTypes(t, fixture, orgID),
	}
	if err := fixture.stores.NodeTypes.Set(rolloutContext(t), nodeType); err != nil {
		t.Fatalf("cannot store node type: %v", err)
	}
	return kind
}

func rolloutEntryTypes(t *testing.T, fixture rolloutFixture, orgID uuid.UUID) []string {
	t.Helper()
	types, err := fixture.stores.NodeTypes.List(rolloutContext(t), orgID)
	if err != nil {
		t.Fatalf("cannot list node types for organization %s: %v", orgID, err)
	}
	keys := make([]string, 0, 1)
	for _, kind := range types {
		if kind.Features.Has(node.FeatureIsEntryPoint) {
			keys = append(keys, kind.TypeKey)
		}
	}
	if len(keys) == 0 {
		t.Fatalf("organization %s does not have an entry point type", orgID)
	}
	return keys
}

func rolloutEntryPoint(t *testing.T, fixture rolloutFixture, workspace datagen.WorkspaceIdentity) uuid.UUID {
	t.Helper()
	for _, typeKey := range rolloutEntryTypes(t, fixture, workspace.OrgID) {
		views, err := fixture.stores.Views.List(rolloutContext(t), node.NodeListQuery{OrgID: workspace.OrgID, NodeType: typeKey})
		if err != nil {
			t.Fatalf("cannot list entry nodes for type %s: %v", typeKey, err)
		}
		for _, view := range views {
			if view.Name == workspace.Name {
				return view.ID
			}
		}
	}
	t.Fatalf("workspace %s does not have an entry node", workspace.Slug)
	return uuid.Nil
}

func rolloutNodeService(fixture rolloutFixture) *service.NodeService {
	stores := fixture.stores
	return service.NewNodeService(stores.Nodes, stores.Views, stores.NodeTypes, stores.PropertyDefs, stores.Relationships, stores.NodeDeleter)
}

// putRolloutNode creates one node under parentID through
// [service.NodeService.Create] and returns its ID.
func putRolloutNode(t *testing.T, fixture rolloutFixture, kind rolloutKind, parentID uuid.UUID, name, text string) uuid.UUID {
	t.Helper()
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("cannot encode property value: %v", err)
	}
	result, err := rolloutNodeService(fixture).Create(rolloutContext(t), service.CreateInput{
		ParentID: parentID, NodeTypeKey: kind.typeKey, Name: name,
		Props: map[string]json.RawMessage{kind.includedKey: encoded},
	})
	if err != nil {
		t.Fatalf("cannot create node under parent %s: %v", parentID, err)
	}
	return result.View.ID
}

func putRolloutCorpus(t *testing.T, fixture rolloutFixture, kind rolloutKind, parentID uuid.UUID, text string, count int) []uuid.UUID {
	t.Helper()
	nodes := make([]uuid.UUID, 0, count)
	for number := range count {
		nodes = append(nodes, putRolloutNode(t, fixture, kind, parentID, fmt.Sprintf("Rollout node %d", number), text))
	}
	return nodes
}

// moveRolloutNode uses [service.NodeService.Update] to move nodeID
// under entry node to. The update replaces the node's child_of edge and
// changes a resource grant.
func moveRolloutNode(t *testing.T, fixture rolloutFixture, nodeID, to uuid.UUID) {
	t.Helper()
	target, err := json.Marshal(to.String())
	if err != nil {
		t.Fatalf("cannot encode property value: %v", err)
	}
	if _, err := rolloutNodeService(fixture).Update(rolloutContext(t), service.UpdateInput{
		NodeID: nodeID, Props: map[string]json.RawMessage{"parent_id": target, "scope_id": target},
	}); err != nil {
		t.Fatalf("cannot move node %s under entry node %s: %v", nodeID, to, err)
	}
}
