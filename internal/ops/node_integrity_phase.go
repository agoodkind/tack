package ops

import (
	"slices"

	"goodkind.io/tack/internal/domain/node"
)

// repairPhases returns entry's phases in execution order. Deletes export
// before deletion. Reports create and verify a comment before removing the
// source property. Other entries complete in one phase.
func repairPhases(entry NodeIntegrityEntry) []node.RepairPhase {
	if entry.Delete != nil {
		return []node.RepairPhase{node.RepairPhaseExported, node.RepairPhaseCompleted}
	}
	if entry.Report != nil {
		return []node.RepairPhase{node.RepairPhaseCommentCreated, node.RepairPhaseCommentVerified, node.RepairPhaseCompleted}
	}
	return []node.RepairPhase{node.RepairPhaseCompleted}
}

// nextRepairPhase returns entry's first phase when current is empty, or the
// phase after current otherwise. It returns an empty phase after completion
// and false if current does not match an entry phase.
func nextRepairPhase(entry NodeIntegrityEntry, current node.RepairPhase) (node.RepairPhase, bool) {
	phases := repairPhases(entry)
	if current == "" {
		return phases[0], true
	}
	position := slices.Index(phases, current)
	if position < 0 {
		return "", false
	}
	if position == len(phases)-1 {
		return "", true
	}
	return phases[position+1], true
}
