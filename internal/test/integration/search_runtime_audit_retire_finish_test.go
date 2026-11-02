//go:build runtimeaudit

package integration_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	retireLateFailureQuery = "ashen weir retire late failure"
	retireStructureLog     = "audit.external.recorded"
	retireAttemptsRemain   = "retirement attempt records remain for the deleted index"
)

// TestSearchRuntimeAuditRetiredSessionLateFailure runs the production search
// worker and pauses FoundationDB after the worker saves its close attempt.
// It resumes FoundationDB at the cleanup failure log, waits past the lease,
// and runs recovery before the worker writes its failed event. The test
// requires one incomplete row and one failed row in audit.events, with
// different event IDs under one operation.
func TestSearchRuntimeAuditRetiredSessionLateFailure(t *testing.T) {
	setShortSessions(t)
	fixture := newSessionAuditFixture(t)
	fixture.putCorpus(t, retireLateFailureQuery, sessionAuditNodes)
	page := fixture.mustSearch(t, fixture.first.Actors[0].Token, retireLateFailureQuery, "")
	session := sessionOfCursor(t, fixture.parentEdgeFixture, page.Cursor)
	source := servingIndex(t, fixture.parentEdgeFixture)
	beginReplacement(t, fixture.parentEdgeFixture)
	rebuilds := fixture.stores.SearchRebuilds(clock.Wall{})
	rebuild, found, err := rebuilds.CurrentRebuild(t.Context())
	if err != nil || !found {
		t.Fatalf("read the index replacement: found %t, %v", found, err)
	}
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireAttemptSavedMessage: func() { setContainerPaused(t, container, true) },
		retireCleanupFailedMessage: func() {
			setContainerPaused(t, container, false)
			waitUntilTime(t, clock.Now().Add(retireAttemptWait))
			if _, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), source, retireSliceLimit); err != nil {
				t.Fatalf("recover the late attempt of index %s: %v", source, err)
			}
		},
	})
	runReplacementThroughFailures(t, fixture.parentEdgeFixture)
	hooks.requireFinished(t)

	rows := fixture.waitForRows(t, fixture.first.OrgID, func(current []audit.Row) bool {
		return len(lostRetireRows(current, session.ID)) == 1 && len(failedCloseRows(current, session.ID)) == 1
	})
	workerID := cli.ServiceActorID(appruntime.SearchWorkerService)
	lost := onlyRow(t, lostRetireRows(rows, session.ID), "the incomplete cleanup result")
	failed := onlyRow(t, failedCloseRows(rows, session.ID), "the late cleanup failure")
	requireRetirementRow(t, lost, workerID, rebuild, source)
	requireRetirementRow(t, failed, workerID, rebuild, source)
	if lost.EventID == failed.EventID || lost.Context.OperationID != failed.Context.OperationID {
		t.Fatalf("incomplete row %s under operation %s and failed row %s under operation %s, want separate rows of one operation",
			lost.EventID, lost.Context.OperationID, failed.EventID, failed.Context.OperationID)
	}
}

func failedCloseRows(rows []audit.Row, sessionID uuid.UUID) []audit.Row {
	selected := []audit.Row{}
	for _, row := range rows {
		target := row.Context.Target
		if row.Action == string(audit.VerbSearchSessionUpdate) && row.Outcome == audit.OutcomeFailed && target != nil &&
			strings.Contains(target.URI, sessionID.String()) && row.Error != nil && row.Error.Code == string(searchdomain.CleanupClose) {
			selected = append(selected, row)
		}
	}
	return selected
}

// TestSearchRuntimeAuditRetireFinishRefusesLateAttempt uses a log hook to
// store an attempt record after the worker deletes the retiring index, as a
// late save does. FinishRebuild must refuse to finish. The worker must finish
// the replacement after recovery handles the record.
func TestSearchRuntimeAuditRetireFinishRefusesLateAttempt(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	fixture.createLeaf(t, fixture.firstEntry, "ashen weir retire finish")
	fixture.drain(t)
	source := servingIndex(t, fixture)
	beginReplacement(t, fixture)
	runReplacementUntil(t, fixture, func(current searchdomain.Rebuild, found bool) bool {
		return found && current.State == searchdomain.RebuildRetiring
	})
	saved := false
	hooks := installRetireLogHooks(t, "", map[string]func(){
		retireStructureLog: func() {
			integration.ReplaceRawValue(t, retireAttemptKey(t, source), []byte(invalidAttemptValue))
			saved = true
		},
	})
	var sliceErr error
	for !saved {
		_, sliceErr = fixture.worker.RunSlice(t.Context())
	}
	hooks.requireFinished(t)
	if sliceErr == nil || !strings.Contains(sliceErr.Error(), retireAttemptsRemain) {
		t.Fatalf("the slice after a late attempt save = %v, want text %q", sliceErr, retireAttemptsRemain)
	}
	_, found, err := fixture.stores.SearchRebuilds(clock.Wall{}).CurrentRebuild(t.Context())
	if err != nil || !found {
		t.Fatalf("the replacement after a late attempt save: found %t, %v, want it unfinished", found, err)
	}
	runReplacementThroughFailures(t, fixture)
}
