package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairExecuteRunsEntriesInManifestOrder checks repair
// execution with dependent and ancestor entries in both manifest orders.
// The test creates a chosen issue under the project with an extra child_of
// edge to the epic. It creates a nested issue under the project with an
// extra child_of edge to the chosen issue.
//
// The dependent entry selects the chosen issue as the nested issue's
// parent. Its frozen graph records the chosen issue's primary digest.
// The ancestor entry selects the epic as the chosen issue's parent and
// changes the chosen issue.
//
// When the manifest lists the dependent entry first, one execution must
// report the entries in manifest order and apply both entries. Each node
// must have its selected parent and exactly one child_of edge. A second
// execution must report both entries unchanged.
//
// With new nodes and a manifest that lists the ancestor entry first, the
// execution must apply the ancestor entry and report the dependent entry
// as conflicted. It must not create a checkpoint for the dependent entry.
// The nested issue must keep both stored child_of edges.
func TestNodeIntegrityRepairExecuteRunsEntriesInManifestOrder(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	childOf := func(targetID uuid.UUID) ops.RepairEdge {
		return ops.RepairEdge{RelationType: node.RelChildOf, TargetID: targetID}
	}
	dependentEntries := func(prefix string) (ops.NodeIntegrityEntry, ops.NodeIntegrityEntry) {
		chosen := createRepairIssue(t, env, graph.projectID, "Chosen parent "+prefix)
		addLegacyParentEdge(t, env, chosen, graph.epicID)
		nested := createRepairIssue(t, env, graph.projectID, "Nested issue "+prefix)
		addLegacyParentEdge(t, env, nested, chosen)
		dependent := parentEntry(prefix+"-nested", nested, chosen, childOf(graph.projectID))
		ancestor := parentEntry(prefix+"-chosen", chosen, graph.epicID, childOf(graph.projectID))
		return dependent, ancestor
	}

	dependent, ancestor := dependentEntries("tack-582-ordered")
	ordered := freezeRepairRequest(t, repairRequest(dependent, ancestor))
	applied, err := executeRepairManifest(t, ordered)
	if err != nil {
		t.Fatalf("execute the ordered manifest: %v", err)
	}
	if len(applied.Entries) != 2 || applied.Entries[0].RepairID != dependent.RepairID || applied.Entries[1].RepairID != ancestor.RepairID {
		t.Fatalf("apply entries = %+v, want %s and then %s", applied.Entries, dependent.RepairID, ancestor.RepairID)
	}
	requireApplyStatus(t, applied, dependent.RepairID, ops.RepairApplyApplied)
	requireApplyStatus(t, applied, ancestor.RepairID, ops.RepairApplyApplied)
	requireParent(t, env, dependent.NodeID, ancestor.NodeID)
	requireParent(t, env, ancestor.NodeID, graph.epicID)
	requireUnchangedRerun(t, ordered)

	dependent, ancestor = dependentEntries("tack-582-reversed")
	reversed := freezeRepairRequest(t, repairRequest(ancestor, dependent))
	applied, err = executeRepairManifest(t, reversed)
	if err != nil {
		t.Fatalf("execute the reversed manifest: %v", err)
	}
	requireApplyStatus(t, applied, ancestor.RepairID, ops.RepairApplyApplied)
	requireApplyStatus(t, applied, dependent.RepairID, ops.RepairApplyConflicted)
	requireCheckpointPhase(t, env, dependent.RepairID, "")
	if targets := childOfTargets(t, env, dependent.NodeID); len(targets) != 2 {
		t.Fatalf("child_of targets of %s after the conflict = %v, want both stored edges", dependent.NodeID, targets)
	}
}
