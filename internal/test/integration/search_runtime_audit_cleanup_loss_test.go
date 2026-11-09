//go:build runtimeaudit

package integration_test

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

type cleanupLossScenario struct {
	continuation     bool
	corruptHeader    bool
	pauseOnDelete    bool
	incompleteDelete bool
	pauseWhen        func(session string) func(slog.Record) bool
	resumeWhen       func(session string) func(slog.Record) bool
	failureHas       []string
	// failureAny lists error text alternatives. The logged error must contain
	// at least one. The point in time delete step result write uses a context
	// with a five second limit and a FoundationDB transaction timeout based on
	// the same deadline. While the FoundationDB container is paused, either
	// limit can stop the write. The transaction timeout returns error code 1031
	// for a timed out transaction. The context limit cancels the transaction
	// and returns "context deadline exceeded" with error code 1025 for a
	// cancelled transaction.
	failureAny []string
}

// TestSearchRuntimeAuditCleanupRecordingLossRefusesPage checks that tack_search
// refuses a page when it cannot record a cleanup failure for an expired session
// with an invalid header.
func TestSearchRuntimeAuditCleanupRecordingLossRefusesPage(t *testing.T) {
	runCleanupLoss(
		t,
		cleanupLossScenario{
			corruptHeader: true,
			pauseWhen: func(session string) func(slog.Record) bool {
				return cleanupLossSessionLog(cleanupLossSweepMessage, session)
			},
			resumeWhen: func(session string) func(slog.Record) bool {
				return cleanupLossSessionLog(cleanupLossAuditMessage, session)
			},
			failureHas: []string{
				"decode search session value",
				"record the failed cleanup step",
				"FoundationDB error code 1031",
			},
		},
	)
}

// TestSearchRuntimeAuditSnapshotCloseRecordingLossRefusesPage checks that
// tack_search refuses a page when it cannot record the point in time delete
// step result for an expired session.
func TestSearchRuntimeAuditSnapshotCloseRecordingLossRefusesPage(t *testing.T) {
	runCleanupLoss(
		t,
		cleanupLossScenario{
			pauseOnDelete:    true,
			incompleteDelete: true,
			resumeWhen:       func(string) func(slog.Record) bool { return cleanupLossStepLog },
			failureHas:       []string{"record external step result"},
			failureAny:       []string{"FoundationDB error code 1031", "context deadline exceeded"},
		},
	)
}

// TestSearchRuntimeAuditCompletedPageCloseRecordingLossRefusesPage checks that
// search refuses the final page when recording the point in time delete result
// fails, records the failure, and replays the committed page on retry.
func TestSearchRuntimeAuditCompletedPageCloseRecordingLossRefusesPage(t *testing.T) {
	runCleanupLoss(
		t,
		cleanupLossScenario{
			continuation:     true,
			pauseOnDelete:    true,
			incompleteDelete: true,
			resumeWhen:       func(string) func(slog.Record) bool { return cleanupLossStepLog },
			failureHas:       []string{"record external step result"},
			failureAny:       []string{"FoundationDB error code 1031", "context deadline exceeded"},
		},
	)
}

func runCleanupLoss(t *testing.T, scenario cleanupLossScenario) {
	t.Helper()
	if !scenario.continuation {
		t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", sessionAuditIdle)
		t.Setenv("OPENSEARCH_SESSION_ABSOLUTE_TIMEOUT", sessionAuditAbsolute)
	}
	fixture := newFailureFixture(t)
	harness, capture := newCaptureHarness(t, fixture.QueryFixture, false)
	before := listOpenPITs(t, fixture)
	t.Cleanup(func() {
		for _, identifier := range addedPITs(listOpenPITs(t, fixture), before) {
			deleteObservedSnapshot(t, fixture, identifier)
		}
	})
	opened := integration.CallSearch(t, harness, failureAuditQuery, "")
	if opened.Cursor == "" {
		t.Fatal("the first search did not return a cursor")
	}
	expired := integration.CursorSession(t, fixture.QueryFixture, opened.Cursor)
	cursor := ""
	if scenario.continuation {
		cursor = opened.Cursor
	} else {
		waitUntilTime(
			t,
			expired.IdleDeadline.Truncate(time.Minute).Add(time.Minute+sessionAuditMargin),
		)
	}
	if scenario.corruptHeader {
		integration.ReplaceRawValue(t, sessionHeaderKey(t, fixture, expired.ID), []byte("{"))
	}
	var pauseWhen func(slog.Record) bool
	if scenario.pauseWhen != nil {
		pauseWhen = scenario.pauseWhen(expired.ID.String())
	}
	gate := newCleanupLossGate(
		t,
		testenv.FoundationDBContainer(testenv.FoundationDB(t)),
		pauseWhen,
		scenario.resumeWhen(expired.ID.String()),
	)
	if scenario.pauseOnDelete {
		cleanupLossPauseOnDelete(capture, gate)
	}
	gate.install()

	page, err := integration.TrySearch(harness, failureAuditQuery, cursor)

	requireCleanupLossRefusal(t, gate, page, err, scenario.failureHas, scenario.failureAny)
	rows := fixture.audited.waitForRows(
		t,
		fixture.Workspaces[0].OrgID,
		func(current []audit.Row) bool {
			failed := cleanupLossReads(current, audit.OutcomeFailed)
			return len(failed) == 1 &&
				len(cleanupLossReads(current, audit.OutcomeCompleted)) == 1 &&
				(scenario.continuation || cleanupLossSessionCreated(
					current,
					failed[0].Context.OperationID,
				)) &&
				(!scenario.incompleteDelete || len(
					cleanupLossIncompleteDeletes(current, expired.ID),
				) == 1)
		},
	)
	refused := cleanupLossReads(rows, audit.OutcomeFailed)[0]
	first := cleanupLossReads(rows, audit.OutcomeCompleted)[0]
	if refused.ActorID != fixture.Workspaces[0].Actors[0].UserID || refused.ActorID != first.ActorID ||
		refused.Context.OperationID == first.Context.OperationID ||
		refused.Error == nil ||
		refused.Error.Code != "audit_recording_failed" {
		t.Fatalf(
			"the failed read event did not preserve the actor, a new operation ID, or the audit_recording_failed code: %+v",
			refused,
		)
	}
	if scenario.incompleteDelete {
		incomplete := cleanupLossIncompleteDeletes(rows, expired.ID)[0]
		if incomplete.ActorID != refused.ActorID || incomplete.Error == nil ||
			incomplete.Error.Code != "step_unfinished" {
			t.Fatalf(
				"the incomplete point in time delete event did not preserve the actor or the step_unfinished code: %+v",
				incomplete,
			)
		}
	}
	if scenario.continuation {
		replayed, replayErr := integration.TrySearch(harness, failureAuditQuery, cursor)
		if replayErr != nil || len(replayed.IDs) == 0 {
			t.Fatalf(
				"retry with the same cursor did not replay the committed page: error %v, result count %d",
				replayErr,
				len(replayed.IDs),
			)
		}
	}
}

func cleanupLossSessionCreated(rows []audit.Row, operationID uuid.UUID) bool {
	return slices.ContainsFunc(
		rows,
		func(row audit.Row) bool {
			return row.Action == string(audit.VerbSearchSessionCreate) &&
				row.Context.Phase == audit.PhaseChange &&
				row.Context.ParentOperationID == operationID
		},
	)
}

func cleanupLossIncompleteDeletes(rows []audit.Row, sessionID uuid.UUID) []audit.Row {
	selected := []audit.Row{}
	for _, row := range snapshotResults(
		rows,
		audit.VerbSearchPointInTimeDelete,
		audit.OutcomeIncomplete,
	) {
		if row.EntityID == sessionID && row.Context.Target.Kind == audit.TargetKindResource {
			selected = append(selected, row)
		}
	}
	return selected
}

func cleanupLossPauseOnDelete(capture *searchCapture, gate *cleanupLossGate) {
	previous := capture.engine.ModifyResponse
	capture.engine.ModifyResponse = func(response *http.Response) error {
		if response.Request.Method == http.MethodDelete &&
			strings.HasSuffix(response.Request.URL.Path, pointInTimePathSuffix) {
			gate.pause()
		}
		if previous == nil {
			return capture.record(response)
		}
		return previous(response)
	}
}

func cleanupLossReads(rows []audit.Row, outcome audit.Outcome) []audit.Row {
	selected := []audit.Row{}
	for _, row := range rows {
		if row.Action == string(audit.VerbNodeSearch) && row.Context.Phase == audit.PhaseRead &&
			row.Outcome == outcome {
			selected = append(selected, row)
		}
	}
	return selected
}
