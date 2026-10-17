package integration_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchTextNamesEveryUndeclaredProperty stores a node with one declared
// property and two properties that no definition declares. Two reads of its
// search text must each return the same exclusion error, and that error must
// contain the node ID and both property names in sorted order.
func TestSearchTextNamesEveryUndeclaredProperty(t *testing.T) {
	env := integration.SetupTestEnv(t)
	env.Stores.EnableSearchWork()
	orgID := uuid.Must(uuid.NewV7())
	declaredName := opaqueName("p")
	laterName := opaqueName("b")
	earlierName := opaqueName("a")
	typeKey := opaqueName("n")
	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: declaredName, Type: node.PropertyType(opaqueName("t")),
		Search: &node.SearchProjection{Include: true, Order: 0, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	if err := env.Stores.PropertyDefs.Set(env.Ctx, definition); err != nil {
		t.Fatalf("store property definition %s: %v", declaredName, err)
	}
	kind := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: typeKey, TypeKey: typeKey, Slug: typeKey, PluralSlug: typeKey + "s",
		PropertyDefIDs: []uuid.UUID{definition.ID}, Features: node.Features{node.FeatureIsEntryPoint},
	}
	if err := env.Stores.NodeTypes.Set(env.Ctx, kind); err != nil {
		t.Fatalf("store node type %s: %v", typeKey, err)
	}
	props := map[string]json.RawMessage{
		declaredName: json.RawMessage(`"declared value"`),
		laterName:    json.RawMessage(`"undeclared later"`),
		earlierName:  json.RawMessage(`"undeclared earlier"`),
	}
	nodeID := uuid.Must(uuid.NewV7())
	now := clock.Now().UTC()
	value := &node.Node{ID: nodeID, OrgID: orgID, NodeType: typeKey, Name: "Undeclared", Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: nodeID, OrgID: orgID, NodeType: typeKey, Name: "Undeclared", Props: props, CreatedAt: now, UpdatedAt: now}
	if err := env.Stores.Nodes.Set(env.Ctx, value, view); err != nil {
		t.Fatalf("store node %s with undeclared properties: %v", nodeID, err)
	}

	reader := env.Stores.SearchContent(env.Stores.SearchPolicySet())
	wantNames := strconv.Quote(earlierName) + ", " + strconv.Quote(laterName)
	var messages []string
	for range 2 {
		_, err := reader.Content(env.Ctx, searchdomain.ContentRequest{
			NodeID: nodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: 128, SearchGeneration: 0,
		})
		if !errors.Is(err, node.ErrInvalidSearchProjection) {
			t.Fatalf("search text error for node %s = %v, want %v", nodeID, err, node.ErrInvalidSearchProjection)
		}
		if !strings.Contains(err.Error(), nodeID.String()) || !strings.Contains(err.Error(), wantNames) {
			t.Fatalf("search text error = %q, want the node ID %s and the names %s", err, nodeID, wantNames)
		}
		messages = append(messages, err.Error())
	}
	if messages[0] != messages[1] {
		t.Fatalf("two reads returned different errors: %q and %q", messages[0], messages[1])
	}
}

// opaqueName returns a unique name that starts with prefix.
func opaqueName(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
}
