package integration_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

// TestCreateEpicUnderEpicStoresOneParentEdge creates an epic in a newly
// seeded organization and a second epic under it through tack_create_epic.
// The second epic must store parent_id equal to the first epic, exactly one
// child_of edge to the first epic, and the first epic's scope_id.
func TestCreateEpicUnderEpicStoresOneParentEdge(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	stores := parentIntegrityStores(t)
	parent := createParentIntegrityNode(t, harness, "tack_create_epic", "Parent epic", "")
	child := createParentIntegrityNode(t, harness, "tack_create_epic", "Epic under the epic", parent)

	requireParents(t, stores, child, parent, []string{parent})
	parentScope := stringProperty(t, storedParentView(t, stores, parent), "scope_id")
	if scope := stringProperty(t, storedParentView(t, stores, child), "scope_id"); scope != parentScope {
		t.Fatalf("epic %s has scope_id %s; the test expects the parent epic's scope_id %s", child, scope, parentScope)
	}
}

// TestEpicParentCycleRefusals builds a chain of three epics through MCP
// tools. The update tool must refuse a move of the top epic under the bottom
// epic and a move of the middle epic under itself. After the refusals, each
// epic must keep its parent_id and its one child_of edge.
func TestEpicParentCycleRefusals(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	stores := parentIntegrityStores(t)
	top := createParentIntegrityNode(t, harness, "tack_create_epic", "Top epic", "")
	middle := createParentIntegrityNode(t, harness, "tack_create_epic", "Middle epic", top)
	bottom := createParentIntegrityNode(t, harness, "tack_create_epic", "Bottom epic", middle)
	projectID := stringProperty(t, storedParentView(t, stores, top), "parent_id")

	for epic, parent := range map[string]string{top: bottom, middle: middle} {
		move := parentIntegrityArguments(harness, "")
		move.NodeID = epic
		move.Properties = datagen.NodeProperties{"parent_id": json.RawMessage(strconv.Quote(parent))}
		if text := harness.CallExpectError(t, "tack_update_epic", move); !strings.Contains(text, "one of its descendants") {
			t.Fatalf("move of epic %s under %s returned %q; the test expects a parent cycle refusal", epic, parent, text)
		}
	}

	for child, parent := range map[string]string{top: projectID, middle: top, bottom: middle} {
		requireParents(t, stores, child, parent, []string{parent})
	}
}
