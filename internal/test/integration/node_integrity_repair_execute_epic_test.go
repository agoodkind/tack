package integration_test

import (
	"encoding/json"
	"maps"
	"strconv"
	"strings"
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
	epic := createTwoParentEpic(t, env, graph)
	stored, err := env.Stores.Views.Get(env.Ctx, epic)
	if err != nil || stored == nil {
		t.Fatalf("read epic %s: %v", epic, err)
	}
	entry := epicParentEntry("tack-582-epic-under-epic", epic, parentEpic, project)
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

// TestNodeIntegrityRepairExecuteEpicParentRefusals uses the epic fixture of
// TestNodeIntegrityRepairExecuteEpicUnderEpic. Execution must fail for a
// result with parent_id at an issue, because the epic type does not list
// issue. After the test stores the earlier epic type with CanLiveUnder
// [project], execution must fail for a result with parent_id at the second
// epic. After ops backfill once-fixed-definitions --execute, a newly frozen
// manifest with the same result must apply. Each failed execution must leave
// the epic's parent_id and child_of edges unchanged.
func TestNodeIntegrityRepairExecuteEpicParentRefusals(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	project, parentEpic := graph.projectID, graph.epicID
	epic := createTwoParentEpic(t, env, graph)
	underIssue := epicParentEntry("tack-582-epic-under-issue", epic, graph.issueID, project, parentEpic)
	requireFailedEpicRepair(t, env, underIssue, epic, project, `cannot live under the parent `+graph.issueID.String()+` of type "issue"`)

	epicType := storedNodeType(t, env, "epic")
	epicType.CanLiveUnder = []string{"project"}
	if err := env.Stores.NodeTypes.Set(env.Ctx, epicType); err != nil {
		t.Fatalf("store the earlier epic node type: %v", err)
	}
	earlier := epicParentEntry("tack-582-epic-earlier-type", epic, parentEpic, project)
	requireFailedEpicRepair(t, env, earlier, epic, project, `cannot live under the parent `+parentEpic.String()+` of type "epic"`)

	runFixedDefinitions(t, "--execute")
	corrected := epicParentEntry("tack-582-epic-corrected-type", epic, parentEpic, project)
	applied, err := executeRepairManifest(t, freezeRepairRequest(t, repairRequest(corrected)))
	if err != nil {
		t.Fatalf("execute the epic repair after the correction: %v", err)
	}
	requireApplyStatus(t, applied, corrected.RepairID, ops.RepairApplyApplied)
	requireParent(t, env, epic, parentEpic)
}

// requireFailedEpicRepair freezes and executes entry. The execution must
// report the entry as failed with a reason that contains want, and the epic
// must keep parent_id at its project and its two child_of edges.
func requireFailedEpicRepair(t *testing.T, env *integration.TestEnv, entry ops.NodeIntegrityEntry, epic, projectID uuid.UUID, want string) {
	t.Helper()
	before := childOfTargets(t, env, epic)
	applied, err := executeRepairManifest(t, freezeRepairRequest(t, repairRequest(entry)))
	if err == nil {
		t.Fatalf("execute repair %s succeeded, want a failed entry", entry.RepairID)
	}
	outcome := requireApplyStatus(t, applied, entry.RepairID, ops.RepairApplyFailed)
	if !strings.Contains(outcome.Reason, want) {
		t.Fatalf("repair %s reason = %q, want %q", entry.RepairID, outcome.Reason, want)
	}
	view, err := env.Stores.Views.Get(env.Ctx, epic)
	if err != nil || view == nil {
		t.Fatalf("read epic %s: %v", epic, err)
	}
	if after := childOfTargets(t, env, epic); len(after) != 2 || after[0] != before[0] || after[1] != before[1] {
		t.Fatalf("epic %s child_of targets = %v after the failed repair, want %v", epic, after, before)
	}
	if parent := propText(t, view.Props, "parent_id"); parent != projectID.String() {
		t.Fatalf("epic %s parent_id = %q after the failed repair %s, want project %s", epic, parent, entry.RepairID, projectID)
	}
}

// createTwoParentEpic creates an epic under the graph's project with a
// description and adds a legacy child_of edge to the graph's epic.
func createTwoParentEpic(t *testing.T, env *integration.TestEnv, graph repairGraph) uuid.UUID {
	t.Helper()
	epic := createRepairNode(t, env, service.CreateInput{
		ParentID: graph.projectID, ScopeID: graph.projectID, NodeTypeKey: "epic", Name: "Epic with two parent edges",
		Props: map[string]json.RawMessage{"description": textValue("Epic repair content")}, ActorID: uuid.New(),
	})
	addLegacyParentEdge(t, env, epic, graph.epicID)
	return epic
}

// epicParentEntry returns an executable entry that sets the parent_id of
// epic to parentID and removes the child_of edges to each target in remove.
func epicParentEntry(repairID string, epic, parentID uuid.UUID, remove ...uuid.UUID) ops.NodeIntegrityEntry {
	edges := make([]ops.RepairEdge, 0, len(remove))
	for _, targetID := range remove {
		edges = append(edges, ops.RepairEdge{RelationType: node.RelChildOf, TargetID: targetID})
	}
	return ops.NodeIntegrityEntry{
		RepairID: repairID, NodeID: epic, NodeType: "epic", Status: ops.RepairEntryExecutable,
		Classes: []string{"multiple_child_of_edges"},
		Evidence: []ops.RepairEvidence{{
			Kind: ops.RepairEvidenceInferred, Reference: "TACK-582 parent mappings", Summary: "The selected parent is the most specific linked container.",
		}},
		Result: ops.RepairResult{
			Props:       map[string]json.RawMessage{"parent_id": json.RawMessage(strconv.Quote(parentID.String()))},
			RemoveEdges: edges,
		},
	}
}
