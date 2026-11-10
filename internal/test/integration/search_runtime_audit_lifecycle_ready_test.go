//go:build runtimeaudit

package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/audit"
)

func lifecycleEvidencePresent(t *testing.T, rows []audit.Row, traversal sessionPages) bool {
	t.Helper()
	sessionID := traversal.stored[0].ID
	pageCount := len(traversal.pages)
	updates := sessionChanges(
		rows,
		sessionID,
		audit.VerbSearchSessionUpdate,
		audit.TargetSearchSession,
	)
	if len(stepRows(rows, sessionID, audit.VerbSearchPointInTimeDelete, "delete")) != 2 ||
		len(updates) != pageCount {
		return false
	}
	if len(
		sessionChanges(rows, sessionID, audit.VerbSearchSessionCreate, audit.TargetSearchSession),
	) == 0 {
		return false
	}
	replayCreates := sessionChanges(
		rows,
		sessionID,
		audit.VerbSearchSessionCreate,
		audit.TargetSearchSessionReplay,
	)
	replayDeletes := sessionChanges(
		rows,
		sessionID,
		audit.VerbSearchSessionDelete,
		audit.TargetSearchSessionReplay,
	)
	if len(replayCreates) < pageCount || len(replayDeletes) < pageCount-1 {
		return false
	}
	visited := visitedCounts(t, rows, sessionID)
	for _, page := range traversal.pages {
		for _, nodeID := range page.IDs {
			if visited[nodeID] == 0 {
				return false
			}
		}
	}
	createSteps := stepRows(rows, sessionID, audit.VerbSearchPointInTimeCreate, "create")
	if len(resultRows(createSteps)) == 0 {
		return false
	}
	for _, verb := range []audit.Verb{
		audit.VerbSearchPointInTimeCreate,
		audit.VerbSearchPointInTimeDelete,
	} {
		phases := pointInTimePhases(rows, sessionID, verb)
		if phases[audit.PhaseIntent] == 0 || phases[audit.PhaseResult] == 0 {
			return false
		}
	}
	reads := searchReadOperations(rows)
	for _, update := range updates {
		if !reads[update.Context.ParentOperationID] {
			return false
		}
	}
	return true
}
