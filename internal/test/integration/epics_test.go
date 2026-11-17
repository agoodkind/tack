package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// requireFailedEpicRepair freezes and executes entry. The execution must
// report the entry as failed with a reason that contains want, and the epic
// must keep parent_id at its project and its two child_of edges.
func requireFailedEpicRepair(
	t *testing.T,
	env *integration.TestEnv,
	entry ops.NodeIntegrityEntry,
	epic uuid.UUID,
	projectID uuid.UUID,
	want string,
) {
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

	if after := childOfTargets(t, env, epic); len(after) != 2 || after[0] != before[0] ||
		after[1] != before[1] {
		t.Fatalf(
			"epic %s child_of targets = %v after the failed repair, want %v",
			epic,
			after,
			before,
		)
	}

	if parent := propText(t, view.Props, "parent_id"); parent != projectID.String() {
		t.Fatalf(
			"epic %s parent_id = %q after the failed repair %s, want project %s",
			epic,
			parent,
			entry.RepairID,
			projectID,
		)
	}
}

// createTwoParentEpic creates an epic under the graph's project with a
// description and adds a legacy child_of edge to the graph's epic.
func createTwoParentEpic(t *testing.T, env *integration.TestEnv, graph repairGraph) uuid.UUID {
	t.Helper()
	epic := createRepairNode(
		t,
		env,
		service.CreateInput{
			ParentID:    graph.projectID,
			ScopeID:     graph.projectID,
			NodeTypeKey: "epic",
			Name:        "Epic with two parent edges",
			Props: map[string]json.RawMessage{
				"description": textValue("Epic repair content"),
			},
			ActorID: uuid.New(),
		},
	)
	addLegacyParentEdge(t, env, epic, graph.epicID)

	return epic
}

// epicParentEntry returns an executable entry that sets the parent of
// epic to parentID and removes the child_of edges to each target in remove.
func epicParentEntry(
	repairID string,
	epic uuid.UUID,
	parentID uuid.UUID,
	remove ...uuid.UUID,
) ops.NodeIntegrityEntry {
	edges := make([]ops.RepairEdge, 0, len(remove))
	for _, targetID := range remove {
		edges = append(edges, ops.RepairEdge{RelationType: node.RelChildOf, TargetID: targetID})
	}

	return ops.NodeIntegrityEntry{
		RepairID: repairID,
		NodeID:   epic,
		NodeType: "epic",
		Status:   ops.RepairEntryExecutable,
		Classes:  []string{"multiple_child_of_edges"},
		Evidence: []ops.RepairEvidence{
			{
				Kind:      ops.RepairEvidenceInferred,
				Reference: "TACK-582 parent mappings",
				Summary:   "The selected parent is the most specific linked container.",
			},
		},
		Result: ops.RepairResult{
			ParentID:    &parentID,
			RemoveEdges: edges,
		},
	}
}
