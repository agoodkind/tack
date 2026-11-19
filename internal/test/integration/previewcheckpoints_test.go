package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairPreviewReadsCheckpoints checks that preview reports
// a final checkpoint as completed, reports the next phase for an earlier
// checkpoint, and preserves unresolved entry status.
func TestNodeIntegrityRepairPreviewReadsCheckpoints(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	fixtureID := createRepairIssue(t, env, graph.projectID, "Exported fixture")
	scopeID := createRepairIssue(t, env, graph.projectID, "Scope exception")
	deleteEntry := ops.NodeIntegrityEntry{
		RepairID: "tack-582-delete-fixture",
		NodeID:   fixtureID,
		NodeType: "issue",
		Status:   ops.RepairEntryExecutable,
		Classes:  []string{"test_fixture"},
		Evidence: []ops.RepairEvidence{
			{
				Kind:      ops.RepairEvidenceDecision,
				Reference: "TACK-582",
				Summary:   "Delete the fixture.",
			},
		},
		Result: ops.RepairResult{},
		Delete: &ops.RepairDelete{ExportFile: repairDirectory(t) + "/fixture.json"},
	}
	unresolved := ops.NodeIntegrityEntry{
		RepairID: "tack-582-scope-exception",
		NodeID:   scopeID,
		NodeType: "issue",
		Status:   ops.RepairEntryUnresolved,
		Classes:  []string{"scope_applicability"},
		Reason:   "TACK-580 must fix the contract first.",
		Evidence: []ops.RepairEvidence{
			{Kind: ops.RepairEvidenceObserved, Reference: "scan", Summary: "scope_id is set."},
		},
		Result: ops.RepairResult{},
	}
	manifest := freezeRepairRequest(
		t,
		repairRequest(graph.selectEpicEntry("tack-582-done"), deleteEntry, unresolved),
	)
	recordCheckpoint(t, env, manifest, 0, node.RepairPhaseCompleted)
	recordCheckpoint(t, env, manifest, 1, node.RepairPhaseExported)

	preview := previewRepairManifest(t, manifest)
	if entry := previewEntry(
		t,
		preview,
		"tack-582-done",
	); entry.Status != ops.RepairPreviewCompleted {
		t.Fatalf("completed entry = %+v, want completed", entry)
	}

	if entry := previewEntry(
		t,
		preview,
		"tack-582-delete-fixture",
	); entry.Status != ops.RepairPreviewInProgress ||
		entry.Phase != node.RepairPhaseExported ||
		entry.NextPhase != node.RepairPhaseCompleted {
		t.Fatalf("exported entry = %+v, want in progress before completed", entry)
	}

	if entry := previewEntry(
		t,
		preview,
		"tack-582-scope-exception",
	); entry.Status != ops.RepairPreviewUnresolved {
		t.Fatalf("unresolved entry = %+v, want unresolved", entry)
	}

	if preview.Counts[ops.RepairPreviewCompleted] != 1 ||
		preview.Counts[ops.RepairPreviewInProgress] != 1 ||
		preview.Counts[ops.RepairPreviewUnresolved] != 1 {
		t.Fatalf("preview counts = %v", preview.Counts)
	}
}
