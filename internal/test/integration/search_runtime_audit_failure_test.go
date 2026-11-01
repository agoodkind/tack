//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

const (
	failureAuditQuery = "tawny flume failure audit"
	// failureAuditResponseBytes bounds one response of the failure test.
	failureAuditResponseBytes = 16384
	// failureAuditIdle and failureAuditAbsolute are the session deadlines of
	// the failure test.
	failureAuditIdle     = 3 * time.Second
	failureAuditAbsolute = 6 * time.Second
)

// failureFixture is a query fixture with a corpus, a relay, and the real audit
// consumer as audit_writer.
type failureFixture struct {
	integration.QueryFixture
	audited sessionAuditFixture
}

func newFailureFixture(t *testing.T) failureFixture {
	t.Helper()
	fixture := integration.NewQueryFixture(t)
	stack := startWriterRoleStack(t)
	ledger := startPublicLedgerOn(t, &publicServer{clusterFile: fixture.Config.FDBClusterFile}, stack)
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entryID := integration.EntryPoint(t, fixture, workspace)
	for range sessionAuditNodes {
		integration.PutOpaqueNode(t, fixture, kind, entryID, failureAuditQuery+" "+uuid.NewString(), failureAuditQuery, "excluded")
	}
	integration.DrainSearchWork(t, fixture.Worker, 10*sessionAuditNodes+100)
	return failureFixture{QueryFixture: fixture, audited: sessionAuditFixture{ledger: ledger}}
}

// openExpired opens a session through searcher and waits past its expiry
// minute.
func (f failureFixture) openExpired(t *testing.T, searcher *service.SearchQueryService, request service.SearchRequest) searchdomain.Session {
	t.Helper()
	page, err := searcher.Search(integration.WithTestOperation(t, t.Context()), request)
	if err != nil {
		t.Fatalf("open the session: %v", err)
	}
	store := f.Stores.SearchSessions(clock.Wall{}, failureAuditIdle)
	session, err := store.Load(integration.WithTestOperation(t, t.Context()), page.SessionID)
	if err != nil {
		t.Fatalf("load session %s: %v", page.SessionID, err)
	}
	waitUntilTime(t, session.IdleDeadline.Truncate(time.Minute).Add(time.Minute+sessionAuditMargin))
	return session
}

// TestSearchRuntimeAuditCleanupFailureRecorded opens a session and waits past
// its expiry minute. A new search then runs under an operation that fails
// event validation. The new session create must commit nothing, and the search
// must delete its point in time under an audited step. The sweep of the expired
// session must fail, leave the session unchanged, and record one failed result
// event for each of the close and delete steps.
func TestSearchRuntimeAuditCleanupFailureRecorded(t *testing.T) {
	fixture := newFailureFixture(t)
	searcher := integration.QueryService(t, fixture.QueryFixture, failureAuditResponseBytes, failureAuditIdle, failureAuditAbsolute)
	request := integration.ServiceRequest(t, fixture.QueryFixture, failureAuditQuery)
	expired := fixture.openExpired(t, searcher, request)

	operation := futureOperation(t)
	failing, err := audit.WithMutationContext(t.Context(), operation)
	if err != nil {
		t.Fatalf("attach the failing operation: %v", err)
	}
	_, err = searcher.Search(failing, request)
	requireChangeEventFailure(t, "Search under the failing operation", err)

	store := fixture.Stores.SearchSessions(clock.Wall{}, failureAuditIdle)
	after, err := store.Load(integration.WithTestOperation(t, t.Context()), expired.ID)
	if err != nil || after.Closing || after.Version != expired.Version {
		t.Fatalf("session %s after the failed sweep = %+v, %v, want the open session at version %d", expired.ID, after, err, expired.Version)
	}
	operationID := operation.Context.OperationID
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(current []audit.Row) bool {
		return len(cleanupFailures(current, expired.ID, searchdomain.CleanupClose)) == 1 &&
			len(cleanupFailures(current, expired.ID, searchdomain.CleanupDelete)) == 1 &&
			len(failedCreateDeletes(current, operationID, expired.ID)) == 1
	})
	closeFailure := onlyRow(t, cleanupFailures(rows, expired.ID, searchdomain.CleanupClose), "the close failure")
	requireCleanupFailureRow(t, closeFailure, audit.VerbSearchSessionUpdate, expired.PrincipalID, operationID)
	deleteFailure := onlyRow(t, cleanupFailures(rows, expired.ID, searchdomain.CleanupDelete), "the delete failure")
	requireCleanupFailureRow(t, deleteFailure, audit.VerbSearchSessionDelete, expired.PrincipalID, operationID)
	requireNoChangeRows(t, rows, operationID)
	step := onlyRow(t, failedCreateDeletes(rows, operationID, expired.ID), "the delete after the failed Create")
	var observed struct {
		PITID string `json:"pit_id"`
	}
	if err := json.Unmarshal(step.Extra, &observed); err != nil || observed.PITID == "" || step.Outcome != audit.OutcomeCompleted {
		t.Fatalf("delete step = %s with outcome %s: %v, want a completed step with a point in time ID", step.Extra, step.Outcome, err)
	}
}

func failedCreateDeletes(rows []audit.Row, operationID, expiredID uuid.UUID) []audit.Row {
	selected := []audit.Row{}
	for _, row := range rows {
		target := row.Context.Target
		if row.Action == string(audit.VerbSearchPointInTimeDelete) && row.Context.ParentOperationID == operationID &&
			row.Context.Phase == audit.PhaseResult && target != nil && target.Kind == audit.TargetKindExternal &&
			!strings.Contains(target.Reference, expiredID.String()) {
			selected = append(selected, row)
		}
	}
	return selected
}

func cleanupFailures(rows []audit.Row, sessionID uuid.UUID, step searchdomain.CleanupStep) []audit.Row {
	selected := []audit.Row{}
	for _, row := range rows {
		if row.EntityID == sessionID && row.Error != nil && row.Error.Code == string(step) {
			selected = append(selected, row)
		}
	}
	return selected
}

func requireCleanupFailureRow(t *testing.T, row audit.Row, verb audit.Verb, ownerID, operationID uuid.UUID) {
	t.Helper()
	eventContext := row.Context
	if row.Outcome != audit.OutcomeFailed || eventContext.OperationClass != audit.OperationDatabaseChange ||
		eventContext.Phase != audit.PhaseResult || eventContext.OperationID != operationID ||
		eventContext.Target.OwnerID != ownerID || row.Action != string(verb) {
		t.Fatalf("cleanup failure row = %+v, context %+v", row, eventContext)
	}
	if strings.Contains(row.Error.Message, changeEventFailure) {
		t.Fatalf("cleanup failure message %q stores the raw error", row.Error.Message)
	}
}

func requireNoChangeRows(t *testing.T, rows []audit.Row, operationID uuid.UUID) {
	t.Helper()
	for _, row := range rows {
		if row.Context.OperationID == operationID && row.Context.Phase == audit.PhaseChange {
			t.Fatalf("the operation must not commit changes, but it recorded change %s on %+v", row.Action, row.Context.Target)
		}
	}
}
