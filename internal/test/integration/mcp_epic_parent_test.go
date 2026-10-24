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

// TestEpicParentRefusals builds a chain of three epics through MCP tools. The
// update tool must refuse a move of the top epic under the bottom epic, a move
// of the middle epic under itself, and a move of the middle epic under an
// issue. The create tool must refuse an epic under a cycle. After the
// refusals, each epic must keep its parent_id and its one child_of edge.
func TestEpicParentRefusals(t *testing.T) {
	harness := integration.NewMCPHarness(t)
	stores := parentIntegrityStores(t)
	top := createParentIntegrityNode(t, harness, "tack_create_epic", "Top epic", "")
	middle := createParentIntegrityNode(t, harness, "tack_create_epic", "Middle epic", top)
	bottom := createParentIntegrityNode(t, harness, "tack_create_epic", "Bottom epic", middle)
	issue := createParentIntegrityNode(t, harness, "tack_create_issue", "Issue beside the epics", "")
	cycle := createParentIntegrityNode(t, harness, "tack_create_cycle", "Cycle beside the epics", "")
	projectID := stringProperty(t, storedParentView(t, stores, top), "parent_id")

	refusals := []struct {
		epic, parent, want string
	}{
		{epic: top, parent: bottom, want: "one of its descendants"},
		{epic: middle, parent: middle, want: "one of its descendants"},
		{epic: middle, parent: issue, want: `cannot live under the parent ` + issue + ` of type "issue"`},
	}
	for _, refusal := range refusals {
		move := parentIntegrityArguments(harness, "")
		move.NodeID = refusal.epic
		move.Properties = datagen.NodeProperties{"parent_id": json.RawMessage(strconv.Quote(refusal.parent))}
		text := harness.CallExpectError(t, "tack_update_epic", move)
		if !strings.Contains(text, refusal.want) {
			t.Fatalf("move of epic %s under %s returned %q; the test expects %q", refusal.epic, refusal.parent, text, refusal.want)
		}
	}
	underCycle := parentIntegrityArguments(harness, harness.Project)
	underCycle.Name = "Epic under a cycle"
	underCycle.Properties = datagen.NodeProperties{"parent_id": json.RawMessage(strconv.Quote(cycle))}
	if text := harness.CallExpectError(t, "tack_create_epic", underCycle); !strings.Contains(text, `of type "cycle"`) {
		t.Fatalf("create of an epic under cycle %s returned %q; the test expects a parent type refusal", cycle, text)
	}

	for child, parent := range map[string]string{top: projectID, middle: top, bottom: middle} {
		requireParents(t, stores, child, parent, []string{parent})
	}
}
