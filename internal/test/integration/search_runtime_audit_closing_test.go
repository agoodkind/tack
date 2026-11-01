//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// closingAuditQuery is the included text of the closing corpus.
const closingAuditQuery = "umber sluice closing audit"

// TestSearchRuntimeAuditExpiredContinuationClosing continues a session with an
// expired cursor after OpenSearch lost its point in time. The ledger must
// store the closing change, the refused node.search event, and a completed
// point in time delete with already_closed set.
func TestSearchRuntimeAuditExpiredContinuationClosing(t *testing.T) {
	setShortSessions(t)
	fixture := newSessionAuditFixture(t)
	fixture.putCorpus(t, closingAuditQuery, sessionAuditNodes)
	owner := fixture.first.Actors[0]
	page := fixture.mustSearch(t, owner.Token, closingAuditQuery, "")
	session := sessionOfCursor(t, fixture.parentEdgeFixture, page.Cursor)
	fixture.loseSnapshot(t, session)
	waitUntilTime(t, session.IdleDeadline)
	if _, err := fixture.searchAs(t, owner.Token, closingAuditQuery, page.Cursor); err == nil || !strings.Contains(err.Error(), expiredCursorText) {
		t.Fatalf("expired continuation returned %v, want an error containing %q", err, expiredCursorText)
	}
	fixture.requireClosingEvents(t, session.ID)
}

// TestSearchRuntimeAuditLostSnapshotClosing continues a session after
// OpenSearch lost its point in time. The ledger must store the closing change
// and the refused node.search event.
func TestSearchRuntimeAuditLostSnapshotClosing(t *testing.T) {
	fixture := newSessionAuditFixture(t)
	fixture.putCorpus(t, closingAuditQuery, sessionAuditNodes)
	owner := fixture.first.Actors[0]
	page := fixture.mustSearch(t, owner.Token, closingAuditQuery, "")
	session := sessionOfCursor(t, fixture.parentEdgeFixture, page.Cursor)
	fixture.loseSnapshot(t, session)
	if _, err := fixture.searchAs(t, owner.Token, closingAuditQuery, page.Cursor); err == nil || !strings.Contains(err.Error(), snapshotEndedText) {
		t.Fatalf("continuation after the lost point in time returned %v, want an error containing %q", err, snapshotEndedText)
	}
	fixture.requireClosingEvents(t, session.ID)
}

// requireClosingEvents requires one closing change with its old and new value,
// one refused read, and a completed delete step with already_closed set.
func (f sessionAuditFixture) requireClosingEvents(t *testing.T, sessionID uuid.UUID) {
	t.Helper()
	rows := f.waitForRows(t, f.first.OrgID, func(current []audit.Row) bool {
		return len(refusedReads(current)) == 1 &&
			len(stepRows(current, sessionID, audit.VerbSearchPointInTimeDelete, "delete")) == 2 &&
			len(closingUpdates(t, current, sessionID)) == 1
	})
	closing := closingUpdates(t, rows, sessionID)[0]
	before, after := deltaMembers(t, closing.Delta.Before), deltaMembers(t, closing.Delta.After)
	if string(before["closing"]) != "false" || string(after["closing"]) != "true" ||
		string(before["version_entry"]) != "true" || string(after["version_entry"]) != "false" {
		t.Fatalf("closing change = %s to %s, want closing and version_entry to change", closing.Delta.Before, closing.Delta.After)
	}
	step := finishedStep(t, stepRows(rows, sessionID, audit.VerbSearchPointInTimeDelete, "delete"))
	var observed struct {
		AlreadyClosed bool `json:"already_closed"`
	}
	if err := json.Unmarshal(step.Extra, &observed); err != nil || !observed.AlreadyClosed || step.Outcome != audit.OutcomeCompleted {
		t.Fatalf("delete step = %s with outcome %s: %v, want a completed step with already_closed", step.Extra, step.Outcome, err)
	}
}

// closingUpdates returns the session updates that change closing.
func closingUpdates(t *testing.T, rows []audit.Row, sessionID uuid.UUID) []audit.Row {
	t.Helper()
	selected := []audit.Row{}
	for _, row := range sessionChanges(rows, sessionID, audit.VerbSearchSessionUpdate, audit.TargetSearchSession) {
		if _, found := deltaMembers(t, row.Delta.After)["closing"]; found {
			selected = append(selected, row)
		}
	}
	return selected
}
