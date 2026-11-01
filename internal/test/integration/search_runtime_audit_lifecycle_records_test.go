//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

func requireReplayRecords(t *testing.T, rows []audit.Row, traversal sessionPages) {
	t.Helper()
	sessionID := traversal.stored[0].ID
	created := sessionChanges(rows, sessionID, audit.VerbSearchSessionCreate, audit.TargetSearchSessionReplay)
	deleted := sessionChanges(rows, sessionID, audit.VerbSearchSessionDelete, audit.TargetSearchSessionReplay)
	if len(created) != len(traversal.pages) || len(deleted) != len(traversal.pages)-1 {
		t.Fatalf("replay rows: %d creates and %d deletes for %d pages", len(created), len(deleted), len(traversal.pages))
	}
	for _, row := range created {
		requireReplayValue(t, row.Delta.After, traversal)
	}
	for _, row := range deleted {
		requireReplayValue(t, row.Delta.Before, traversal)
	}
}

func requireReplayValue(t *testing.T, value json.RawMessage, traversal sessionPages) {
	t.Helper()
	var replay struct {
		Version   int         `json:"version"`
		ResultIDs []uuid.UUID `json:"result_ids"`
	}
	if err := json.Unmarshal(value, &replay); err != nil {
		t.Fatalf("decode replay value %s: %v", value, err)
	}
	if !slices.Equal(replay.ResultIDs, traversal.pages[replay.Version].IDs) {
		t.Fatalf("replay version %d stores %v, want %v", replay.Version, replay.ResultIDs, traversal.pages[replay.Version].IDs)
	}
}

func requireVisitedRecords(t *testing.T, rows []audit.Row, traversal sessionPages) {
	t.Helper()
	sessionID := traversal.stored[0].ID
	recorded := map[uuid.UUID]int{}
	for _, row := range sessionChanges(rows, sessionID, audit.VerbSearchSessionCreate, audit.TargetSearchSessionVisited) {
		var visited struct {
			SessionID uuid.UUID `json:"session_id"`
			NodeID    uuid.UUID `json:"node_id"`
		}
		if err := json.Unmarshal(row.Delta.After, &visited); err != nil || visited.SessionID != sessionID {
			t.Fatalf("visited value %s of session %s: %v", row.Delta.After, sessionID, err)
		}
		recorded[visited.NodeID]++
	}
	for _, page := range traversal.pages {
		for _, nodeID := range page.IDs {
			if recorded[nodeID] != 1 {
				t.Fatalf("node %s has %d visited rows, want 1", nodeID, recorded[nodeID])
			}
		}
	}
}

func requirePointInTimeSteps(t *testing.T, rows []audit.Row, session searchdomain.Session) {
	t.Helper()
	created := stepRows(rows, session.ID, audit.VerbSearchPointInTimeCreate, "create")
	deleted := stepRows(rows, session.ID, audit.VerbSearchPointInTimeDelete, "delete")
	var observed struct {
		PITID         string `json:"pit_id"`
		AlreadyClosed bool   `json:"already_closed"`
	}
	if err := json.Unmarshal(finishedStep(t, created).Extra, &observed); err != nil || observed.PITID == "" {
		t.Fatalf("create step evidence = %+v: %v", observed, err)
	}
	if err := json.Unmarshal(finishedStep(t, deleted).Extra, &observed); err != nil || observed.PITID == "" || observed.AlreadyClosed {
		t.Fatalf("delete step evidence = %+v: %v, want an open point in time", observed, err)
	}
	for _, verb := range []audit.Verb{audit.VerbSearchPointInTimeCreate, audit.VerbSearchPointInTimeDelete} {
		phases := map[audit.Phase]int{}
		for _, row := range rows {
			if row.Action == string(verb) && row.EntityID == session.ID && row.Context.OperationClass == audit.OperationExternal {
				phases[row.Context.Phase]++
			}
		}
		if phases[audit.PhaseIntent] != 1 || phases[audit.PhaseResult] != 1 {
			t.Fatalf("%s operation phases = %v, want one intent and one result", verb, phases)
		}
	}
}

func requireSearchParents(t *testing.T, rows []audit.Row, sessionID uuid.UUID) {
	t.Helper()
	reads := map[uuid.UUID]bool{}
	for _, row := range rows {
		if row.Action == string(audit.VerbNodeSearch) {
			reads[row.Context.OperationID] = true
		}
	}
	for _, update := range sessionChanges(rows, sessionID, audit.VerbSearchSessionUpdate, audit.TargetSearchSession) {
		if !reads[update.Context.ParentOperationID] || update.Context.ParentOperationID == update.Context.OperationID {
			t.Fatalf("update %s states parent %s, want a node.search operation", update.EventID, update.Context.ParentOperationID)
		}
	}
}
