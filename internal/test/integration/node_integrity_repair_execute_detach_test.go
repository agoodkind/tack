package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairExecuteRemovesListedNonAncestorEdges checks that
// execution removes listed child_of edges outside the selected parent's
// ancestor chain. The test creates two epics in a project and an issue
// under the first epic with one child_of edge to that epic. The first entry
// selects the second epic and lists removal of the edge to the first epic.
// The test also creates a chosen issue under the project with an extra
// child_of edge to the first epic, and a nested issue with child_of edges
// to the project, the first epic, and the chosen issue. The second entry
// selects the chosen issue and lists removals of the edges to the project
// and the first epic. The first epic is not on the chosen issue's parent_id
// chain. The test requires one execution to apply both entries and leave
// each node with its selected parent_id and exactly one child_of edge to
// that parent. A second execution must report every entry unchanged without
// changing stored keys.
func TestNodeIntegrityRepairExecuteRemovesListedNonAncestorEdges(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	project, epic := graph.projectID, graph.epicID
	otherEpic := createRepairNode(t, env, service.CreateInput{ParentID: project, ScopeID: project, NodeTypeKey: "epic", Name: "Other epic", ActorID: uuid.New()})
	moved := createRepairNode(t, env, service.CreateInput{ParentID: epic, ScopeID: project, NodeTypeKey: "issue", Name: "Issue under the first epic", ActorID: uuid.New()})
	chosen := createRepairIssue(t, env, project, "Chosen parent issue")
	addLegacyParentEdge(t, env, chosen, epic)
	nested := createRepairIssue(t, env, project, "Issue with three parent edges")
	addLegacyParentEdge(t, env, nested, epic)
	addLegacyParentEdge(t, env, nested, chosen)

	childOf := func(targetID uuid.UUID) ops.RepairEdge {
		return ops.RepairEdge{RelationType: node.RelChildOf, TargetID: targetID}
	}
	entries := []ops.NodeIntegrityEntry{
		parentEntry("tack-582-other-epic", moved, otherEpic, childOf(epic)),
		parentEntry("tack-582-chosen-issue", nested, chosen, childOf(project), childOf(epic)),
	}
	manifest := freezeRepairRequest(t, repairRequest(entries...))

	applied, err := executeRepairManifest(t, manifest)
	if err != nil {
		t.Fatalf("execute the non-ancestor removals: %v", err)
	}
	for _, entry := range entries {
		requireApplyStatus(t, applied, entry.RepairID, ops.RepairApplyApplied)
	}
	requireParent(t, env, moved, otherEpic)
	requireParent(t, env, nested, chosen)
	requireUnchangedRerun(t, manifest)
}
