package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairExportsBeforeDelete executes a manifest with three
// delete entries through ops backfill once-node-integrity --execute.
// The first entry must write a readable export file with mode 0600, delete
// its node, and record the completed checkpoint. The second entry must fail
// on a missing export directory, keep its node, and must not record a
// checkpoint. After a change to its node, the third entry must report a
// conflict, keep its node, and must not write an export file or record a
// checkpoint. A second execution must report the first entry as unchanged,
// fail the second entry again, and report the third entry as conflicted
// again. It must keep the number of stored keys unchanged.
func TestNodeIntegrityRepairExportsBeforeDelete(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	exportDir := t.TempDir()
	exported := deleteFixtureEntry(t, env, graph, "tack-582-export", filepath.Join(exportDir, "export.json"))
	missing := deleteFixtureEntry(t, env, graph, "tack-582-missing-dir", filepath.Join(exportDir, "absent", "export.json"))
	stale := deleteFixtureEntry(t, env, graph, "tack-582-stale-export", filepath.Join(exportDir, "stale.json"))
	manifest := freezeRepairRequest(t, repairRequest(exported, missing, stale))
	renameNode(t, env, stale.NodeID)
	manifestPath := writeRepairDocument(t, "manifest.json", manifest)

	result, err := runRepairCommand(t, "--execute", "--manifest", manifestPath)
	if err == nil || result.Applied == nil {
		t.Fatalf("execute = %v with result %+v, want a failed entry and an apply result", err, result)
	}
	requireApplyEntry(t, *result.Applied, exported.RepairID, ops.RepairApplyApplied, "")
	requireApplyEntry(t, *result.Applied, missing.RepairID, ops.RepairApplyFailed, node.RepairPhaseExported)
	requireApplyEntry(t, *result.Applied, stale.RepairID, ops.RepairApplyConflicted, "")
	requireExportFile(t, exported.Delete.ExportFile, manifest.Entries[0], graph.projectID)
	if _, statErr := os.Stat(stale.Delete.ExportFile); !os.IsNotExist(statErr) {
		t.Fatalf("stat stale export = %v, want no file", statErr)
	}
	requireRepairNodeAbsent(t, env, exported.NodeID)
	requireRepairNodePresent(t, env, missing.NodeID)
	requireRepairNodePresent(t, env, stale.NodeID)
	requireCheckpointPhase(t, env, exported.RepairID, node.RepairPhaseCompleted)
	requireCheckpointPhase(t, env, missing.RepairID, "")

	before := storedKeyValues(t)
	rerun, _ := runRepairCommand(t, "--execute", "--manifest", manifestPath)
	if rerun.Applied == nil {
		t.Fatal("the second execution returned no apply result")
	}
	requireApplyEntry(t, *rerun.Applied, exported.RepairID, ops.RepairApplyUnchanged, "")
	requireApplyEntry(t, *rerun.Applied, missing.RepairID, ops.RepairApplyFailed, node.RepairPhaseExported)
	requireApplyEntry(t, *rerun.Applied, stale.RepairID, ops.RepairApplyConflicted, "")
	requireCheckpointPhase(t, env, exported.RepairID, node.RepairPhaseCompleted)
	requireCheckpointPhase(t, env, missing.RepairID, "")
	requireCheckpointPhase(t, env, stale.RepairID, "")
	if len(storedKeyValues(t)) != len(before) {
		t.Fatal("the second execution changed the number of stored keys")
	}
}

func deleteFixtureEntry(t *testing.T, env *integration.TestEnv, graph repairGraph, repairID, exportFile string) ops.NodeIntegrityEntry {
	t.Helper()
	return ops.NodeIntegrityEntry{
		RepairID: repairID, NodeID: createRepairIssue(t, env, graph.projectID, "Fixture "+repairID), NodeType: "issue",
		Status: ops.RepairEntryExecutable, Classes: []string{"test_fixture"},
		Evidence: []ops.RepairEvidence{{Kind: ops.RepairEvidenceDecision, Reference: "TACK-582", Summary: "Delete the fixture."}},
		Result:   ops.RepairResult{}, Delete: &ops.RepairDelete{ExportFile: exportFile},
	}
}

func requireApplyEntry(t *testing.T, applied ops.NodeIntegrityApply, repairID string, status ops.RepairApplyStatus, next node.RepairPhase) {
	t.Helper()
	for _, entry := range applied.Entries {
		if entry.RepairID != repairID {
			continue
		}
		if entry.Status != status || entry.NextPhase != next {
			t.Fatalf("apply entry %s = %+v, want status %s and next phase %q", repairID, entry, status, next)
		}
		return
	}
	t.Fatalf("apply result has no entry %s: %+v", repairID, applied.Entries)
}

func requireCheckpointPhase(t *testing.T, env *integration.TestEnv, repairID string, phase node.RepairPhase) {
	t.Helper()
	checkpoint, err := env.Stores.Repairs().RepairCheckpoint(env.Ctx, repairID)
	if err != nil {
		t.Fatalf("read checkpoint %s: %v", repairID, err)
	}
	stored := node.RepairPhase("")
	if checkpoint != nil {
		stored = checkpoint.Phase
	}
	if stored != phase {
		t.Fatalf("checkpoint %s phase = %q, want %q", repairID, stored, phase)
	}
}

func requireExportFile(t *testing.T, path string, entry ops.NodeIntegrityEntry, parentID uuid.UUID) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat export %s: %v", path, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("export mode = %v, want 0600", info.Mode().Perm())
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read export %s: %v", path, err)
	}
	var document struct {
		NodeID        uuid.UUID       `json:"node_id"`
		PrimaryDigest string          `json:"primary_digest"`
		Primary       json.RawMessage `json:"primary"`
		Relationships []struct {
			SourceID     uuid.UUID `json:"source_id"`
			RelationType string    `json:"relation_type"`
			TargetID     uuid.UUID `json:"target_id"`
		} `json:"relationships"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("decode export %s: %v", path, err)
	}
	if document.NodeID != entry.NodeID || document.PrimaryDigest != entry.Source.PrimaryDigest || len(document.Primary) == 0 {
		t.Fatalf("export records node %s with digest %s, want node %s with digest %s",
			document.NodeID, document.PrimaryDigest, entry.NodeID, entry.Source.PrimaryDigest)
	}
	for _, relationship := range document.Relationships {
		if relationship.SourceID == entry.NodeID && relationship.RelationType == node.RelChildOf && relationship.TargetID == parentID {
			return
		}
	}
	t.Fatalf("export relationships = %+v, want the child_of edge to %s", document.Relationships, parentID)
}
