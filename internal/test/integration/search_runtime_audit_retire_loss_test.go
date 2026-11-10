//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/testenv"
)

const retireLossQuery = "ashen weir retire loss"

// TestSearchRuntimeAuditRetiredSessionLostResult runs the production search
// worker through an index replacement. The test pauses FoundationDB after the
// worker saves the close attempt for an expired session and resumes it after
// the failure event write fails. The worker retries and finishes the replacement.
// The test requires the audit.events table to contain one incomplete session
// update for the first attempt before a separate successful close operation.
// Both rows must include the worker actor, replacement parent operation, rebuild
// ID, source index, and initiating actor.
func TestSearchRuntimeAuditRetiredSessionLostResult(t *testing.T) {
	setShortSessions(t)
	fixture := newSessionAuditFixture(t)
	fixture.putCorpus(t, retireLossQuery, sessionAuditNodes)
	owner := fixture.first.Actors[0]
	page := fixture.mustSearch(t, owner.Token, retireLossQuery, "")
	session := sessionOfCursor(t, fixture.parentEdgeFixture, page.Cursor)
	source := servingIndex(t, fixture.parentEdgeFixture)
	beginReplacement(t, fixture.parentEdgeFixture)
	rebuild, found, err := fixture.stores.SearchRebuilds(clock.Wall{}).CurrentRebuild(t.Context())
	if err != nil || !found {
		t.Fatalf("read the index replacement: found %t, %v", found, err)
	}
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	gate := installRetirePauseGate(t, container, session.ID.String(), retireAttemptSavedMessage)
	runReplacementThroughFailures(t, fixture.parentEdgeFixture)
	gate.requireFinished(t)

	rows := fixture.waitForRows(t, fixture.first.OrgID, func(current []audit.Row) bool {
		return len(lostRetireRows(current, session.ID)) == 1 && len(closingUpdates(t, current, session.ID)) == 1 &&
			len(sessionChanges(current, session.ID, audit.VerbSearchSessionDelete, audit.TargetSearchSession)) == 1
	})
	workerID := cli.ServiceActorID(appruntime.SearchWorkerService)
	lost := onlyRow(t, lostRetireRows(rows, session.ID), "the lost cleanup result")
	closed := closingUpdates(t, rows, session.ID)[0]
	requireRetirementRow(t, lost, workerID, rebuild, source)
	requireRetirementRow(t, closed, workerID, rebuild, source)
	var evidence retireFailureEvidence
	if err := json.Unmarshal(lost.Extra, &evidence); err != nil || evidence.Cleanup != transactionTimedOutCode || evidence.Recording != transactionTimedOutCode {
		t.Fatalf("lost attempt evidence = %s, %v, want cleanup and recording code %q", lost.Extra, err, transactionTimedOutCode)
	}
	if lost.Context.OperationID == closed.Context.OperationID || !lost.Context.StartedAt.Before(closed.Context.StartedAt) ||
		!lost.EventTime.Before(closed.EventTime) {
		t.Fatalf("lost attempt %s started at %s with event time %s; the successful retry %s started at %s with event time %s; want an earlier separate operation",
			lost.Context.OperationID, lost.Context.StartedAt, lost.EventTime, closed.Context.OperationID, closed.Context.StartedAt, closed.EventTime)
	}
}

func lostRetireRows(rows []audit.Row, sessionID uuid.UUID) []audit.Row {
	selected := []audit.Row{}
	for _, row := range rows {
		target := row.Context.Target
		if row.Action == string(audit.VerbSearchSessionUpdate) && row.Outcome == audit.OutcomeIncomplete && target != nil &&
			strings.Contains(target.URI, sessionID.String()) && row.Error != nil && row.Error.Code == retireResultNotSaved {
			selected = append(selected, row)
		}
	}
	return selected
}

func runReplacementThroughFailures(t *testing.T, fixture parentEdgeFixture) {
	t.Helper()
	rebuilds := fixture.stores.SearchRebuilds(clock.Wall{})
	deadline := clock.Now().Add(replacementDeadline)
	for clock.Now().Before(deadline) {
		_, found, err := rebuilds.CurrentRebuild(t.Context())
		if err != nil {
			t.Fatalf("read index replacement: %v", err)
		}
		if !found {
			return
		}
		claimed, err := fixture.worker.RunSlice(t.Context())
		if err != nil {
			t.Logf("the search worker slice failed: %v", err)
		}
		if !claimed {
			waitUntilTime(t, clock.Now().Add(replacementPause))
		}
	}
	t.Fatalf("the index replacement did not finish within %s", replacementDeadline)
}
