package integration_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairExecuteRunsEntriesInManifestOrder checks both
// manifest orders with new nodes for each manifest.
// Each fixture creates a chosen issue and a nested issue under the project.
// The chosen issue also has a child_of edge to the epic. The nested issue
// also has a child_of edge to the chosen issue.
// The dependent entry selects the chosen issue as the nested issue's parent.
// The ancestor entry selects the epic as the chosen issue's parent.
// Each frozen graph records edges in both directions. The ancestor entry's
// graph records the nested issue's primary digest. The dependent entry's
// graph records the chosen issue's primary digest.
// Each manifest places a different entry first. The test requires execution
// to report entries in manifest order. The first entry must apply, and its
// node must have the selected parent and exactly one child_of edge.
// The second entry must report a stale graph conflict and must not have a
// checkpoint. Its node must keep both stored child_of edges.
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
	requireFirstEntryApplied(t, env, dependent, ancestor, ancestor.NodeID)

	dependent, ancestor = dependentEntries("tack-582-reversed")
	requireFirstEntryApplied(t, env, ancestor, dependent, graph.epicID)
}

func requireFirstEntryApplied(t *testing.T, env *integration.TestEnv, first, second ops.NodeIntegrityEntry, firstParentID uuid.UUID) {
	t.Helper()
	manifest := freezeRepairRequest(t, repairRequest(first, second))
	applied, err := executeRepairManifest(t, manifest)
	if err != nil {
		t.Fatalf("execute the manifest with %s first: %v", first.RepairID, err)
	}
	if len(applied.Entries) != 2 || applied.Entries[0].RepairID != first.RepairID || applied.Entries[1].RepairID != second.RepairID {
		t.Fatalf("apply entries = %+v, want %s and then %s", applied.Entries, first.RepairID, second.RepairID)
	}
	requireApplyStatus(t, applied, first.RepairID, ops.RepairApplyApplied)
	conflicted := requireApplyStatus(t, applied, second.RepairID, ops.RepairApplyConflicted)
	if !strings.Contains(conflicted.Reason, "stale graph") {
		t.Fatalf("conflicted entry %s reason = %q, want a stale graph", second.RepairID, conflicted.Reason)
	}
	requireParent(t, env, first.NodeID, firstParentID)
	requireCheckpointPhase(t, env, second.RepairID, "")
	if targets := childOfTargets(t, env, second.NodeID); len(targets) != 2 {
		t.Fatalf("child_of targets of %s after the conflict = %v, want both stored edges", second.NodeID, targets)
	}
}
