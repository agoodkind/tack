//go:build runtimeaudit

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// TestSearchRuntimeAuditInterruptedSnapshot records an incomplete result
// after the real engine applies a create or delete and the caller stops
// waiting for the proxy to forward the response.
func TestSearchRuntimeAuditInterruptedSnapshot(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		for _, interrupt := range []string{"cancel", "client_timeout"} {
			t.Run(method+"/"+interrupt, func(t *testing.T) {
				fixture := newFailureFixture(t)
				t.Setenv("OPENSEARCH_MAX_RETRIES", "0")
				if interrupt == "client_timeout" {
					t.Setenv("OPENSEARCH_REQUEST_TIMEOUT", "5s")
				}
				harness, capture := newCaptureHarness(t, fixture.QueryFixture, false)
				cursor := ""
				verb := audit.VerbSearchPointInTimeCreate
				if method == http.MethodDelete {
					page := integration.CallSearch(t, harness, failureAuditQuery, "")
					if page.Cursor == "" {
						t.Fatal("the delete fixture did not open a continuing session")
					}
					cursor, verb = page.Cursor, audit.VerbSearchPointInTimeDelete
				}
				response, release := holdSnapshotResponse(t, capture, method)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan snapshotSearchResult, 1)
				go func() {
					done <- callSnapshotSearch(ctx, fixture, harness, cursor)
				}()
				observed := waitSnapshotResponse(t, response)
				if method == http.MethodPost {
					requirePITOpen(t, fixture.QueryFixture, []string{observed.id})
					t.Cleanup(func() { deleteObservedSnapshot(t, fixture, observed.id) })
				} else {
					requirePITsClosed(t, fixture.QueryFixture, []string{observed.id})
				}
				if interrupt == "cancel" {
					cancel()
				}
				select {
				case result := <-done:
					t.Logf("interrupted search: status=%d error=%v result=%q", result.status, result.err, result.text)
				case <-time.After(sessionTimeoutHoldLimit):
					t.Fatal("the interrupted search did not finish")
				}
				if interrupt == "client_timeout" && ctx.Err() != nil {
					t.Fatalf("the request context ended during a client timeout: %v", ctx.Err())
				}
				release()
				rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(rows []audit.Row) bool {
					results := snapshotResults(rows, verb, audit.OutcomeIncomplete)
					return len(results) == 2 && snapshotResultsHaveIntents(rows, results)
				})
				requireInterruptedSnapshotRows(t, fixture, rows, verb)
			})
		}
	}
}

// TestSearchRuntimeAuditDelayedSnapshotResult pauses real FoundationDB for
// twelve seconds after OpenSearch creates a point in time. The step result
// write times out after five seconds. The enclosing operation must record
// an incomplete result after FoundationDB resumes. The failed search must
// then record deletion of its known point in time.
func TestSearchRuntimeAuditDelayedSnapshotResult(t *testing.T) {
	const stall = 12 * time.Second
	fixture := newFailureFixture(t)
	harness, capture := newCaptureHarness(t, fixture.QueryFixture, false)
	response, release := holdSnapshotResponse(t, capture, http.MethodPost)
	done := make(chan error, 1)
	go func() {
		_, err := integration.TrySearch(harness, failureAuditQuery, "")
		done <- err
	}()
	observed := waitSnapshotResponse(t, response)
	t.Cleanup(func() { deleteObservedSnapshot(t, fixture, observed.id) })
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	paused := true
	setContainerPaused(t, container, true)
	t.Cleanup(func() {
		if paused {
			setContainerPaused(t, container, false)
		}
	})
	release()
	time.Sleep(stall)
	setContainerPaused(t, container, false)
	paused = false
	select {
	case err := <-done:
		if err == nil || err.Error() != sessionTimeoutUnavailableText {
			t.Fatalf("search after a %s FoundationDB stall returned %v, want %q", stall, err, sessionTimeoutUnavailableText)
		}
	case <-time.After(sessionTimeoutRecovery):
		t.Fatal("the search did not finish after FoundationDB resumed")
	}
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(rows []audit.Row) bool {
		incompleteCreates := snapshotResults(rows, audit.VerbSearchPointInTimeCreate, audit.OutcomeIncomplete)
		completedCreates := snapshotResults(rows, audit.VerbSearchPointInTimeCreate, audit.OutcomeCompleted)
		completedDeletes := snapshotResults(rows, audit.VerbSearchPointInTimeDelete, audit.OutcomeCompleted)
		return len(incompleteCreates) == 1 && len(completedDeletes) == 2 &&
			snapshotResultsHaveIntents(rows, incompleteCreates) &&
			snapshotResultsHaveIntents(rows, completedCreates) &&
			snapshotResultsHaveIntents(rows, completedDeletes)
	})
	result := onlyRow(t, snapshotResults(rows, audit.VerbSearchPointInTimeCreate, audit.OutcomeIncomplete), "the delayed operation result")
	if result.EntityID == uuid.Nil || result.Error == nil || result.Error.Code != "step_unfinished" || result.Context.Target.Kind != audit.TargetKindResource {
		t.Fatalf("the delayed result lost the session target or unfinished step: %+v", result)
	}
	for _, finished := range snapshotResults(rows, audit.VerbSearchPointInTimeCreate, audit.OutcomeCompleted) {
		// FoundationDB can commit the step result write after the write times out.
		// A completed external step row may appear. The enclosing operation must
		// remain incomplete.
		if finished.Context.Target == nil || finished.Context.Target.Kind != audit.TargetKindExternal {
			t.Fatalf("the incomplete resource operation recorded a completed create result: %s", describeRows([]audit.Row{finished}))
		}
		requireSnapshotIdentity(t, fixture, rows, finished)
		var created struct {
			PITID string `json:"pit_id"`
		}
		if err := json.Unmarshal(finished.Extra, &created); err != nil || created.PITID != observed.id ||
			finished.Context.ParentOperationID != result.Context.OperationID {
			t.Fatalf("the completed create step does not match the incomplete operation or observed point in time %s: %+v", observed.id, finished)
		}
	}
	requireSnapshotIdentity(t, fixture, rows, result)
	var deleteOperations []audit.Row
	for _, deleted := range snapshotResults(rows, audit.VerbSearchPointInTimeDelete, audit.OutcomeCompleted) {
		requireSnapshotIdentity(t, fixture, rows, deleted)
		if deleted.Context.Target.Kind == audit.TargetKindResource {
			deleteOperations = append(deleteOperations, deleted)
		}
	}
	operation := onlyRow(t, deleteOperations, "the completed delete operation")
	deleted := finishedStep(t, stepRows(rows, result.EntityID, audit.VerbSearchPointInTimeDelete, "delete"))
	if operation.EntityID != result.EntityID || operation.Context.Target.ID != result.EntityID ||
		operation.Context.Target.OwnerID != fixture.Workspaces[0].Actors[0].UserID ||
		deleted.Context.Target.Kind != audit.TargetKindExternal || deleted.Outcome != audit.OutcomeCompleted ||
		deleted.Context.ParentOperationID != operation.Context.OperationID {
		t.Fatalf("the completed cleanup lost its original session owner or operation relationship: operation=%+v step=%+v", operation, deleted)
	}
	var evidence struct {
		PITID string `json:"pit_id"`
	}
	if err := json.Unmarshal(deleted.Extra, &evidence); err != nil || evidence.PITID != observed.id {
		t.Fatalf("the audited cleanup does not identify the known point in time %s: %s; error=%v", observed.id, deleted.Extra, err)
	}
	requirePITsClosed(t, fixture.QueryFixture, []string{observed.id})
}
