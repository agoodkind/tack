package ops

import (
	"encoding/json"
	"path/filepath"
	"strconv"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
)

// datagenRepairRequest returns one executable entry for each record in
// fixture. The smoke comment entry exports the comment into directory
// before deleting it.
func datagenRepairRequest(fixture datagen.RepairFixture, directory repairDirectory) NodeIntegrityRequest {
	prefix := "qa-repair-" + fixture.Suffix + "-"
	parent := func(parentID uuid.UUID) map[string]json.RawMessage {
		return map[string]json.RawMessage{repairParentProperty: datagenRepairText(parentID.String())}
	}
	entries := []NodeIntegrityEntry{
		datagenRepairEntry(prefix+"multiple-edges", fixture.MultipleEdges, "issue", RepairEvidenceInferred, RepairResult{
			Props:       parent(fixture.EpicID),
			RemoveEdges: []RepairEdge{{RelationType: node.RelChildOf, TargetID: fixture.ProjectID}},
		}, "multiple_child_of_edges"),
		datagenRepairEntry(prefix+"legacy-status", fixture.LegacyStatus, "issue", RepairEvidenceDecision, RepairResult{
			Props: map[string]json.RawMessage{
				"state_id": datagenRepairText(fixture.DoneState.String()), "priority": datagenRepairText("high"), "status": json.RawMessage("null"),
			},
		}, "legacy_status", "select_label"),
		datagenRepairEntry(prefix+"printed-parent", fixture.PrintedParent, "issue", RepairEvidenceObserved, RepairResult{
			Props: parent(fixture.EpicID),
		}, "printed_parent"),
		datagenRepairEntry(prefix+"absent-parent", fixture.AbsentParent, "issue", RepairEvidenceInferred, RepairResult{
			Props: parent(fixture.ProjectID),
		}, "absent_parent_uuid"),
		datagenRepairEntry(prefix+"duplicate-notice", fixture.Notice, "comment", RepairEvidenceObserved, RepairResult{
			Props: parent(fixture.NoticeTarget),
		}, "absent_parent"),
	}
	smoke := datagenRepairEntry(prefix+"smoke-comment", fixture.SmokeComment, "comment", RepairEvidenceDecision, RepairResult{}, "test_fixture")
	smoke.Delete = &RepairDelete{ExportFile: filepath.Join(directory.root, prefix+"smoke-comment.json")}
	entries = append(entries, smoke)
	return NodeIntegrityRequest{Version: nodeIntegrityManifestVersion, Ticket: nodeIntegrityTicket, Entries: entries}
}

func datagenRepairEntry(repairID string, nodeID uuid.UUID, nodeType string, kind RepairEvidenceKind, result RepairResult, classes ...string) NodeIntegrityEntry {
	return NodeIntegrityEntry{
		RepairID: repairID, NodeID: nodeID, NodeType: nodeType, Status: RepairEntryExecutable, Classes: classes,
		Evidence: []RepairEvidence{{Kind: kind, Reference: "ops qa datagen repair", Summary: "The datagen repair step wrote this record for the repair class in this entry."}},
		Result:   result,
	}
}

func datagenRepairText(text string) json.RawMessage {
	return json.RawMessage(strconv.Quote(text))
}
