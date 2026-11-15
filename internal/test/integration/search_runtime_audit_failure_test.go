//go:build runtimeaudit

package integration_test

import (
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
	ledger := startPublicLedgerOn(
		t,
		&publicServer{clusterFile: fixture.Config.FDBClusterFile},
		stack,
	)
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entryID := integration.EntryPoint(t, fixture, workspace)
	for range sessionAuditNodes {
		integration.PutOpaqueNode(
			t,
			fixture,
			kind,
			entryID,
			failureAuditQuery+" "+uuid.NewString(),
			failureAuditQuery,
			"excluded",
		)
	}
	integration.DrainSearchWork(t, fixture.Worker, 10*sessionAuditNodes+100)
	return failureFixture{QueryFixture: fixture, audited: sessionAuditFixture{ledger: ledger}}
}

// openExpired opens a session through searcher and waits past its expiry
// minute.
func (f failureFixture) openExpired(
	t *testing.T,
	searcher *service.SearchQueryService,
	request service.SearchRequest,
) searchdomain.Session {
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

// TestSearchRuntimeAuditScope checks that public searches record reads without internal session events.
func TestSearchRuntimeAuditScope(t *testing.T) {
	fixture := newFailureFixture(t)
	page := integration.CallSearch(t, fixture.Harness, failureAuditQuery, "")
	if len(page.IDs) == 0 {
		t.Fatal("public search did not return results")
	}
	if page.Cursor != "" {
		integration.CallSearch(t, fixture.Harness, failureAuditQuery, page.Cursor)
	}
	fixture.audited.waitForRows(
		t,
		fixture.Workspaces[0].OrgID,
		func(rows []audit.Row) bool {
			return len(cleanupLossReads(rows, audit.OutcomeCompleted)) > 0
		},
	)
	waitForTopicConsumed(t, fixture.audited.ledger)
	for _, row := range fixture.audited.ledgerRows(t, fixture.Workspaces[0].OrgID) {
		switch audit.Verb(row.Action) {
		case audit.VerbSearchSessionCreate, audit.VerbSearchSessionUpdate,
			audit.VerbSearchSessionDelete, audit.VerbSearchPointInTimeCreate,
			audit.VerbSearchPointInTimeDelete:
			t.Fatalf("public search recorded internal operation %s", row.Action)
		}
	}
}

const searchUnavailableCode = "search_unavailable"

func cleanupLossReads(rows []audit.Row, outcome audit.Outcome) []audit.Row {
	var selected []audit.Row
	for _, row := range rows {
		if row.Action == string(audit.VerbNodeSearch) && row.Outcome == outcome {
			selected = append(selected, row)
		}
	}
	return selected
}
