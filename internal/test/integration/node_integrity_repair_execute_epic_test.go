package integration_test

import (
	"encoding/json"
	"maps"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairExecuteEpicUnderEpic stores an epic with parent_id
// at its project and child_of edges to the project and to a second epic. The
// test freezes a repair request with --request that sets parent_id to the
// second epic and removes the project edge, then executes the manifest with
// --manifest --execute. The epic must end with one child_of edge to the
// second epic, the same scope_id, name, and other properties, and the outbox
// must contain one node.update event and one relationship.remove event for
// the epic.
func TestNodeIntegrityRepairExecuteEpicUnderEpic(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	project, parentEpic := graph.projectID, graph.epicID
	epic := createRepairNode(t, env, service.CreateInput{
		ParentID: project, ScopeID: project, NodeTypeKey: "epic", Name: "Epic with two parent edges",
		Props: map[string]json.RawMessage{"description": textValue("Epic repair content")}, ActorID: uuid.New(),
	})
	addLegacyParentEdge(t, env, epic, parentEpic)
	stored, err := env.Stores.Views.Get(env.Ctx, epic)
	if err != nil || stored == nil {
		t.Fatalf("read epic %s: %v", epic, err)
	}
	entry := ops.NodeIntegrityEntry{
		RepairID: "tack-582-epic-under-epic", NodeID: epic, NodeType: "epic", Status: ops.RepairEntryExecutable,
		Classes: []string{"multiple_child_of_edges"},
		Evidence: []ops.RepairEvidence{{
			Kind: ops.RepairEvidenceInferred, Reference: "TACK-582 parent mappings", Summary: "The second epic is the most specific linked container.",
		}},
		Result: ops.RepairResult{
			Props:       map[string]json.RawMessage{"parent_id": json.RawMessage(strconv.Quote(parentEpic.String()))},
			RemoveEdges: []ops.RepairEdge{{RelationType: node.RelChildOf, TargetID: project}},
		},
	}
	manifest := freezeRepairRequest(t, repairRequest(entry))
	mark := outboxTail(t, env)

	applied, err := executeRepairManifest(t, manifest)
	if err != nil {
		t.Fatalf("execute the epic repair: %v", err)
	}
	requireApplyStatus(t, applied, entry.RepairID, ops.RepairApplyApplied)
	view := requireParent(t, env, epic, parentEpic)
	if view.Name != stored.Name {
		t.Fatalf("epic %s name = %q after the repair, want %q", epic, view.Name, stored.Name)
	}
	wantProps := maps.Clone(stored.Props)
	wantProps["parent_id"] = textValue(parentEpic.String())
	if !maps.EqualFunc(view.Props, wantProps, func(left, right json.RawMessage) bool { return string(left) == string(right) }) {
		t.Fatalf("epic %s properties = %v after the repair, want %v", epic, view.Props, wantProps)
	}
	if scope := propText(t, view.Props, "scope_id"); scope != project.String() {
		t.Fatalf("epic %s scope_id = %q, want project %s", epic, scope, project)
	}
	events := outboxEventsAfter(t, env, mark)
	for _, verb := range []audit.Verb{audit.VerbNodeUpdate, audit.VerbRelationshipRemove} {
		if found := changeEventsFor(events, verb, epic); len(found) != 1 {
			t.Fatalf("%s outbox events for epic %s = %d, want 1", verb, epic, len(found))
		}
	}
	requireUnchangedRerun(t, manifest)
}
