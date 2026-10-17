package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairExecuteDeletesAfterVerifiedExport executes two
// delete entries for test comments. The first entry must export the comment,
// verify the export, and delete exactly that comment. The second entry uses
// an export directory that does not exist. Its export must fail, and the
// comment must remain without a checkpoint. After the directory exists, a
// second execution must export and delete the second comment and must leave
// the first entry unchanged. The issue and its other comment must remain.
func TestNodeIntegrityRepairExecuteDeletesAfterVerifiedExport(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	issueID := createRepairIssue(t, env, graph.projectID, "Issue with smoke comments")
	kept := createRepairComment(t, env, issueID, graph.projectID, "Useful comment")
	exportDir := t.TempDir()
	missingDir := filepath.Join(exportDir, "later")
	first := commentDeleteEntry("tack-582-delete-smoke", createRepairComment(t, env, issueID, graph.projectID, "Smoke comment"), filepath.Join(exportDir, "smoke.json"))
	second := commentDeleteEntry("tack-582-delete-later", createRepairComment(t, env, issueID, graph.projectID, "Smoke comment"), filepath.Join(missingDir, "later.json"))
	manifest := freezeRepairRequest(t, repairRequest(first, second))

	applied, err := executeRepairManifest(t, manifest)
	if err == nil {
		t.Fatal("execute with a missing export directory succeeded")
	}
	requireApplyStatus(t, applied, first.RepairID, ops.RepairApplyApplied)
	requireApplyStatus(t, applied, second.RepairID, ops.RepairApplyFailed)
	requireExportFile(t, first.Delete.ExportFile, manifest.Entries[0], issueID)
	requireRepairNodeAbsent(t, env, first.NodeID)
	requireCheckpointPhase(t, env, first.RepairID, node.RepairPhaseCompleted)
	requireRepairNodePresent(t, env, second.NodeID)
	requireCheckpointPhase(t, env, second.RepairID, "")

	if err := os.Mkdir(missingDir, 0o700); err != nil {
		t.Fatalf("create export directory %s: %v", missingDir, err)
	}
	resumed, err := executeRepairManifest(t, manifest)
	if err != nil {
		t.Fatalf("execute after creating the export directory: %v", err)
	}
	requireApplyStatus(t, resumed, first.RepairID, ops.RepairApplyUnchanged)
	outcome := requireApplyStatus(t, resumed, second.RepairID, ops.RepairApplyApplied)
	if len(outcome.Committed) != 2 || outcome.Committed[0] != node.RepairPhaseExported || outcome.Committed[1] != node.RepairPhaseCompleted {
		t.Fatalf("resumed entry committed %v, want exported then completed", outcome.Committed)
	}
	requireExportFile(t, second.Delete.ExportFile, manifest.Entries[1], issueID)
	requireRepairNodeAbsent(t, env, second.NodeID)
	requireRepairNodePresent(t, env, issueID)
	requireRepairNodePresent(t, env, kept)
	requireUnchangedRerun(t, manifest)
}

// TestNodeIntegrityRepairExecuteDeleteStopsChangedSource checks a delete retry.
// The test creates a fixture issue with a comment as its hierarchy child and
// freezes a delete entry for the issue. The first execution must export the
// issue, record the exported checkpoint, and fail deletion on the child.
// The test then renames the issue. The second execution must report the entry
// as conflicted and preserve the exported checkpoint, issue, and child comment.
func TestNodeIntegrityRepairExecuteDeleteStopsChangedSource(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	entry := deleteFixtureEntry(t, env, graph, "tack-582-delete-changed", filepath.Join(t.TempDir(), "changed.json"))
	childID := createRepairComment(t, env, entry.NodeID, graph.projectID, "Comment under the fixture issue")
	manifest := freezeRepairRequest(t, repairRequest(entry))

	applied, err := executeRepairManifest(t, manifest)
	if err == nil {
		t.Fatal("execute a delete of a node with a hierarchy child succeeded")
	}
	requireApplyStatus(t, applied, entry.RepairID, ops.RepairApplyFailed)
	requireCheckpointPhase(t, env, entry.RepairID, node.RepairPhaseExported)
	requireExportFile(t, entry.Delete.ExportFile, manifest.Entries[0], graph.projectID)
	renameNode(t, env, entry.NodeID)

	resumed, err := executeRepairManifest(t, manifest)
	if err != nil {
		t.Fatalf("execute after the source changed: %v", err)
	}
	requireApplyStatus(t, resumed, entry.RepairID, ops.RepairApplyConflicted)
	requireCheckpointPhase(t, env, entry.RepairID, node.RepairPhaseExported)
	requireRepairNodePresent(t, env, entry.NodeID)
	requireRepairNodePresent(t, env, childID)
}

func commentDeleteEntry(repairID string, commentID uuid.UUID, exportFile string) ops.NodeIntegrityEntry {
	return ops.NodeIntegrityEntry{
		RepairID: repairID, NodeID: commentID, NodeType: "comment", Status: ops.RepairEntryExecutable,
		Classes: []string{"test_fixture"},
		Evidence: []ops.RepairEvidence{{
			Kind: ops.RepairEvidenceDecision, Reference: "TACK-582 test comment", Summary: "The comment is a post-deploy smoke fixture.",
		}},
		Result: ops.RepairResult{}, Delete: &ops.RepairDelete{ExportFile: exportFile},
	}
}

func createRepairComment(t *testing.T, env *integration.TestEnv, parentID, projectID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	return createRepairNode(t, env, service.CreateInput{
		ParentID: parentID, ScopeID: projectID, NodeTypeKey: "comment", Name: name,
		Props: map[string]json.RawMessage{"description": textValue(name)}, ActorID: uuid.New(),
	})
}

func requireRepairNodeAbsent(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) {
	t.Helper()
	primary, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, nodeID)
	if err != nil || primary != nil {
		t.Fatalf("read primary record %s = %+v, %v, want no record", nodeID, primary, err)
	}
	view, err := env.Stores.Views.Get(env.Ctx, nodeID)
	if err != nil || view != nil {
		t.Fatalf("read view %s = %+v, %v, want no view", nodeID, view, err)
	}
}
