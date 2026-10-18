package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairExecuteParentMappings executes one entry for each
// parent mapping of the backfill specification. Each node must end with the
// selected parent_id, one child_of edge to that parent, and its project
// scope.
//
// The test records the stored child_of edges for the issue that selects
// the epic, the issue that keeps the epic, and the deep issue that selects
// the middle issue. Each recorded edge already targets its node's selected
// parent and must keep its CreatedBy and CreatedAt values after the repair.
//
// A second execution must not commit a phase.
func TestNodeIntegrityRepairExecuteParentMappings(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	project, epic := graph.projectID, graph.epicID

	retained := createRepairNode(t, env, service.CreateInput{ParentID: epic, ScopeID: project, NodeTypeKey: "issue", Name: "Issue under the epic", ActorID: uuid.New()})
	addLegacyParentEdge(t, env, retained, project)
	middle := createRepairNode(t, env, service.CreateInput{ParentID: epic, ScopeID: project, NodeTypeKey: "issue", Name: "Middle issue", ActorID: uuid.New()})
	deep := createRepairIssue(t, env, project, "Deep issue")
	addLegacyParentEdge(t, env, deep, epic)
	addLegacyParentEdge(t, env, deep, middle)
	printed := createRepairNode(t, env, service.CreateInput{ParentID: epic, ScopeID: project, NodeTypeKey: "issue", Name: "Printed parent", ActorID: uuid.New()})
	setLegacyProps(t, env, printed, map[string]json.RawMessage{"parent_id": textValue("REPAIR-1")})
	printedProject := createRepairIssue(t, env, project, "Printed parent with a project edge")
	setLegacyProps(t, env, printedProject, map[string]json.RawMessage{"parent_id": textValue("REPAIR-1")})
	absent := createRepairIssue(t, env, project, "Absent parent")
	setLegacyProps(t, env, absent, map[string]json.RawMessage{"parent_id": textValue(uuid.New().String())})
	kept := []node.Relationship{childOfEdge(t, env, graph.issueID, epic), childOfEdge(t, env, retained, epic), childOfEdge(t, env, deep, middle)}

	childOf := func(targetID uuid.UUID) ops.RepairEdge {
		return ops.RepairEdge{RelationType: node.RelChildOf, TargetID: targetID}
	}
	entries := []ops.NodeIntegrityEntry{
		graph.selectEpicEntry("tack-582-select-epic"),
		parentEntry("tack-582-retain-parent", retained, uuid.Nil, childOf(project)),
		parentEntry("tack-582-deep-parent", deep, middle, childOf(project), childOf(epic)),
		parentEntry("tack-582-printed-parent", printed, epic),
		parentEntry("tack-582-printed-project", printedProject, epic, childOf(project)),
		parentEntry("tack-582-absent-parent", absent, project),
	}
	manifest := freezeRepairRequest(t, repairRequest(entries...))

	applied, err := executeRepairManifest(t, manifest)
	if err != nil {
		t.Fatalf("execute parent mappings: %v", err)
	}
	want := map[uuid.UUID]uuid.UUID{graph.issueID: epic, retained: epic, deep: middle, printed: epic, printedProject: epic, absent: project}
	for _, entry := range entries {
		outcome := requireApplyStatus(t, applied, entry.RepairID, ops.RepairApplyApplied)
		if len(outcome.Committed) != 1 || outcome.Committed[0] != node.RepairPhaseCompleted {
			t.Fatalf("apply entry %s committed %v, want only completed", entry.RepairID, outcome.Committed)
		}
		view := requireParent(t, env, entry.NodeID, want[entry.NodeID])
		if scope := propText(t, view.Props, "scope_id"); scope != project.String() {
			t.Fatalf("node %s scope_id = %q, want project %s", entry.NodeID, scope, project)
		}
		requireCheckpointPhase(t, env, entry.RepairID, node.RepairPhaseCompleted)
	}
	for _, before := range kept {
		after := childOfEdge(t, env, before.SourceID, before.TargetID)
		if after.CreatedBy != before.CreatedBy || !after.CreatedAt.Equal(before.CreatedAt) {
			t.Fatalf("child_of edge from %s to %s = %+v after the repair, want the stored edge %+v", before.SourceID, before.TargetID, after, before)
		}
	}
	requireParent(t, env, middle, epic)
	requireUnchangedRerun(t, manifest)
}

// childOfEdge lists the stored child_of edges of sourceID through the
// relationship store and returns the edge that targets targetID. It fails
// the test when the node does not have that edge.
func childOfEdge(t *testing.T, env *integration.TestEnv, sourceID, targetID uuid.UUID) node.Relationship {
	t.Helper()
	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, sourceID, node.RelChildOf)
	if err != nil {
		t.Fatalf("list child_of edges of %s: %v", sourceID, err)
	}
	for _, edge := range edges {
		if edge.TargetID == targetID {
			return *edge
		}
	}
	t.Fatalf("node %s has no child_of edge to %s", sourceID, targetID)
	return node.Relationship{}
}

// TestNodeIntegrityRepairExecuteStopsChangedSource changes a node after the
// freeze. The execution must report the entry as conflicted and must leave
// the parent, the edges, and the checkpoint unchanged.
func TestNodeIntegrityRepairExecuteStopsChangedSource(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	manifest := freezeRepairRequest(t, repairRequest(graph.selectEpicEntry("tack-582-changed-source")))
	renameNode(t, env, graph.issueID)

	applied, err := executeRepairManifest(t, manifest)
	if err != nil {
		t.Fatalf("execute a changed source: %v", err)
	}
	requireApplyStatus(t, applied, "tack-582-changed-source", ops.RepairApplyConflicted)
	requireCheckpointPhase(t, env, "tack-582-changed-source", "")
	view, err := env.Stores.Views.Get(env.Ctx, graph.issueID)
	if err != nil || view == nil || propText(t, view.Props, "parent_id") != graph.projectID.String() {
		t.Fatalf("issue view = %+v, %v, want parent_id %s", view, err, graph.projectID)
	}
	if targets := childOfTargets(t, env, graph.issueID); len(targets) != 2 {
		t.Fatalf("issue child_of targets = %v, want both legacy edges", targets)
	}
}

// parentEntry returns an executable entry that sets parentID as the parent
// and removes the listed edges. A nil parentID keeps the current parent.
func parentEntry(repairID string, nodeID, parentID uuid.UUID, remove ...ops.RepairEdge) ops.NodeIntegrityEntry {
	result := ops.RepairResult{Props: map[string]json.RawMessage{}, RemoveEdges: remove}
	if parentID != uuid.Nil {
		result.Props["parent_id"] = textValue(parentID.String())
	}
	return ops.NodeIntegrityEntry{
		RepairID: repairID, NodeID: nodeID, NodeType: "issue", Status: ops.RepairEntryExecutable,
		Classes: []string{"parent_mapping"},
		Evidence: []ops.RepairEvidence{{
			Kind: ops.RepairEvidenceInferred, Reference: "TACK-582 parent mappings", Summary: "The selected parent is the most specific linked container.",
		}},
		Result: result,
	}
}

// addLegacyParentEdge adds a child_of edge from sourceID to targetID without
// changing parent_id.
func addLegacyParentEdge(t *testing.T, env *integration.TestEnv, sourceID, targetID uuid.UUID) {
	t.Helper()
	if err := env.Stores.Relationships.Add(env.Ctx, &node.Relationship{
		OrgID: env.OrgID, SourceID: sourceID, RelationType: node.RelChildOf, TargetID: targetID,
		CreatedBy: uuid.New(), CreatedAt: time.Now().UTC(), Props: nil,
	}); err != nil {
		t.Fatalf("add the legacy child_of edge from %s to %s: %v", sourceID, targetID, err)
	}
}
