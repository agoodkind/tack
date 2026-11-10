package ops

import (
	"fmt"

	"goodkind.io/tack/internal/domain/node"
)

func previewRelationshipChanges(changes node.RelationshipChanges) []RepairRelationshipChange {
	planned := make([]RepairRelationshipChange, 0, len(changes.Remove)+len(changes.Add))
	for _, relationship := range changes.Remove {
		planned = append(planned, RepairRelationshipChange{
			Action:       RepairRelationshipRemove,
			SourceID:     relationship.SourceID,
			RelationType: relationship.RelationType,
			TargetID:     relationship.TargetID,
		})
	}
	for _, relationship := range changes.Add {
		planned = append(planned, RepairRelationshipChange{
			Action:       RepairRelationshipAdd,
			SourceID:     relationship.SourceID,
			RelationType: relationship.RelationType,
			TargetID:     relationship.TargetID,
		})
	}
	return planned
}

func summarizeParentReferencePreview(preview *RepairPreview) string {
	if preview == nil || preview.ChosenCandidate == nil {
		return "no parent reference candidate was selected"
	}
	if !preview.NeedsRepair {
		return "parent already matches selected candidate"
	}
	if _, ok := preview.PlannedProps["parent_id"]; ok {
		return fmt.Sprintf("set parent_id=%s from %s", preview.ChosenCandidate.NodeID, preview.ChosenCandidate.SourceField)
	}
	return fmt.Sprintf("repair parent relationship for %s from %s", preview.ChosenCandidate.NodeID, preview.ChosenCandidate.SourceField)
}
