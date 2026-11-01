//go:build runtimeaudit

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// failureAuditQuery is the included text of the failure corpus.
	failureAuditQuery = "tawny flume failure audit"
	// failureAuditResponseBytes bounds one response of the failure test.
	failureAuditResponseBytes = 16384
	// failureAuditIdle and failureAuditAbsolute are the session deadlines of
	// the failure test.
	failureAuditIdle     = 3 * time.Second
	failureAuditAbsolute = 6 * time.Second
)

// TestSearchRuntimeAuditCleanupFailureRecorded opens a session and waits past
// its expiry minute. A new search then runs under an operation that fails
// event validation. The new session create must commit nothing. The sweep of
// the expired session must fail, leave the session unchanged, and record one
// failed result event with the code session_close_failed.
func TestSearchRuntimeAuditCleanupFailureRecorded(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	stack := startWriterRoleStack(t)
	ledger := startPublicLedgerOn(t, &publicServer{clusterFile: fixture.Config.FDBClusterFile}, stack)
	audited := sessionAuditFixture{ledger: ledger}
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entryID := integration.EntryPoint(t, fixture, workspace)
	for range sessionAuditNodes {
		integration.PutOpaqueNode(t, fixture, kind, entryID, failureAuditQuery+" "+uuid.NewString(), failureAuditQuery, "excluded")
	}
	integration.DrainSearchWork(t, fixture.Worker, 10*sessionAuditNodes+100)
	searcher := integration.QueryService(t, fixture, failureAuditResponseBytes, failureAuditIdle, failureAuditAbsolute)
	request := integration.ServiceRequest(t, fixture, failureAuditQuery)
	page, err := searcher.Search(integration.WithTestOperation(t, t.Context()), request)
	if err != nil {
		t.Fatalf("open the session: %v", err)
	}
	store := fixture.Stores.SearchSessions(clock.Wall{}, failureAuditIdle)
	expired, err := store.Load(integration.WithTestOperation(t, t.Context()), page.SessionID)
	if err != nil {
		t.Fatalf("load session %s: %v", page.SessionID, err)
	}
	waitUntilTime(t, expired.IdleDeadline.Truncate(time.Minute).Add(time.Minute+sessionAuditMargin))

	operation := futureOperation(t)
	failing, err := audit.WithMutationContext(t.Context(), operation)
	if err != nil {
		t.Fatalf("attach the failing operation: %v", err)
	}
	_, err = searcher.Search(failing, request)
	requireChangeEventFailure(t, "Search under the failing operation", err)

	after, err := store.Load(integration.WithTestOperation(t, t.Context()), expired.ID)
	if err != nil || after.Closing || after.Version != expired.Version {
		t.Fatalf("session %s after the failed sweep = %+v, %v, want the open session at version %d", expired.ID, after, err, expired.Version)
	}
	rows := audited.waitForRows(t, workspace.OrgID, func(current []audit.Row) bool {
		return len(cleanupFailures(current, expired.ID, searchdomain.CleanupClose)) == 1
	})
	failure := onlyRow(t, cleanupFailures(rows, expired.ID, searchdomain.CleanupClose), "the cleanup failure")
	requireCleanupFailureRow(t, failure, expired, operation.Context.OperationID)
	requireNoChangeRows(t, rows, operation.Context.OperationID)
}

// cleanupFailures returns the failed result rows of step on sessionID.
func cleanupFailures(rows []audit.Row, sessionID uuid.UUID, step searchdomain.CleanupStep) []audit.Row {
	selected := []audit.Row{}
	for _, row := range rows {
		if row.EntityID == sessionID && row.Error != nil && row.Error.Code == string(step) {
			selected = append(selected, row)
		}
	}
	return selected
}

func requireCleanupFailureRow(t *testing.T, row audit.Row, session searchdomain.Session, operationID uuid.UUID) {
	t.Helper()
	eventContext := row.Context
	if row.Outcome != audit.OutcomeFailed || eventContext.OperationClass != audit.OperationDatabaseChange ||
		eventContext.Phase != audit.PhaseResult || eventContext.OperationID != operationID ||
		eventContext.Target.OwnerID != session.PrincipalID || row.Action != string(audit.VerbSearchSessionUpdate) {
		t.Fatalf("cleanup failure row = %+v, context %+v", row, eventContext)
	}
	if strings.Contains(row.Error.Message, changeEventFailure) {
		t.Fatalf("cleanup failure message %q stores the raw error", row.Error.Message)
	}
}

// requireNoChangeRows requires the failing operation to commit no change.
func requireNoChangeRows(t *testing.T, rows []audit.Row, operationID uuid.UUID) {
	t.Helper()
	for _, row := range rows {
		if row.Context.OperationID == operationID && row.Context.Phase == audit.PhaseChange {
			t.Fatalf("the failing operation committed change %s on %+v", row.Action, row.Context.Target)
		}
	}
}
