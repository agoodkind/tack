//go:build runtimeaudit

package integration_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// sessionAuditIdle and sessionAuditAbsolute are the short session
	// deadlines of the tests that expire a session.
	sessionAuditIdle     = "3s"
	sessionAuditAbsolute = "6s"
	// sessionAuditMargin follows the end of an expiry minute before a sweep.
	sessionAuditMargin = time.Second
	// sessionAuditNodes exceeds the 25 results of one page.
	sessionAuditNodes = 30
)

// sessionAuditFixture is one production graph with its relay, the real audit
// consumer as audit_writer, and a redactor login for audit.pii.
type sessionAuditFixture struct {
	parentEdgeFixture
	ledger       publicLedger
	redactor     *audit.Redactor
	redactorPool *pgxpool.Pool
}

func newSessionAuditFixture(t *testing.T) sessionAuditFixture {
	t.Helper()
	fixture := newParentEdgeFixture(t)
	stack := startWriterRoleStack(t)
	ledger := startPublicLedgerOn(t, &publicServer{clusterFile: fixture.cfg.FDBClusterFile}, stack)
	redactor, pool := openRedactor(t, stack)
	return sessionAuditFixture{
		parentEdgeFixture: fixture,
		ledger:            ledger,
		redactor:          redactor,
		redactorPool:      pool,
	}
}

// setShortSessions sets the short session deadlines in the environment that
// the fixture graph reads.
func setShortSessions(t *testing.T) {
	t.Helper()
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", sessionAuditIdle)
	t.Setenv("OPENSEARCH_SESSION_ABSOLUTE_TIMEOUT", sessionAuditAbsolute)
}

// putCorpus creates count leaves that store query and indexes them.
func (f sessionAuditFixture) putCorpus(t *testing.T, query string, count int) {
	t.Helper()
	for number := range count {
		f.createLeaf(t, f.firstEntry, fmt.Sprintf("%s %02d", query, number))
	}
	f.drain(t)
}

// searchAs calls tack_search through the MCP driver with token as the caller's bearer credential.
// The audit ledger attributes the read to the user who owns token. searchAs returns the parsed
// integration.SearchPage and wraps tool call errors with the query text.
func (f sessionAuditFixture) searchAs(
	t *testing.T,
	token string,
	query string,
	cursor string,
) (integration.SearchPage, error) {
	t.Helper()
	result, err := f.driver.Call(
		t.Context(),
		token,
		"tack_search",
		datagen.ToolArguments{
			WorkspaceReference: f.first.Slug,
			Query:              query,
			Cursor:             cursor,
		},
	)
	if err != nil {
		return integration.SearchPage{}, fmt.Errorf("search %q: %w", query, err)
	}
	return integration.ParseSearchPage(t.Context(), result.Text(), f.identities(f.first))
}

func (f sessionAuditFixture) mustSearch(
	t *testing.T,
	token string,
	query string,
	cursor string,
) integration.SearchPage {
	t.Helper()
	page, err := f.searchAs(t, token, query, cursor)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

// loseSnapshot deletes the point in time of session through the OpenSearch
// adapter. OpenSearch confirms a later idempotent delete with HTTP 200.
func (f sessionAuditFixture) loseSnapshot(t *testing.T, session searchdomain.Session) {
	t.Helper()
	ranker := f.adapter.Ranker(search.RankerSettings{
		KeepAlive:  time.Minute,
		TokenBytes: 64 << 10,
		BatchSize:  100,
	})
	if err := ranker.Close(t.Context(), session.Snapshot); err != nil {
		t.Fatalf("delete the point in time of session %s: %v", session.ID, err)
	}
	client := integration.OpenSearchClient(
		t,
		testenv.OpenSearchWithMemory(t, cohortEngineMemoryBytes),
	)
	listed, err := client.PointInTime.GetAll(t.Context(), nil)
	if err != nil {
		t.Fatalf(
			"list points in time after deleting the snapshot of session %s: %v",
			session.ID,
			err,
		)
	}
	for _, pointInTime := range listed.PITs {
		if pointInTime.PITID.String() == session.Snapshot.PITID {
			t.Fatalf(
				"the lost-snapshot fixture left the point in time of session %s open",
				session.ID,
			)
		}
	}
}

func (f sessionAuditFixture) ledgerRows(t *testing.T, orgID uuid.UUID) []audit.Row {
	t.Helper()
	filter := audit.QueryFilter{
		OrgID:  orgID,
		Oldest: f.ledger.since,
		Latest: clock.Now().Add(time.Hour),
		Limit:  audit.MaxQueryPageLimit,
	}
	rows := []audit.Row{}
	cursor := ""
	for {
		page, err := f.ledger.stack.reader.QueryPage(t.Context(), filter, cursor)
		if err != nil {
			t.Fatalf("read the ledger rows of organization %s: %v", orgID, err)
		}
		rows = append(rows, page.Rows...)
		if page.NextCursor == "" {
			return rows
		}
		cursor = page.NextCursor
	}
}

func (f sessionAuditFixture) waitForRows(
	t *testing.T,
	orgID uuid.UUID,
	ready func([]audit.Row) bool,
) []audit.Row {
	t.Helper()
	deadline := clock.Now().Add(publicDeliveryTimeout)
	for {
		rows := f.ledgerRows(t, orgID)
		if ready(rows) {
			return rows
		}
		if clock.Now().After(deadline) {
			t.Fatalf(
				"the ledger of organization %s stayed short of the expected rows for %s; it stores %d rows: %s",
				orgID,
				publicDeliveryTimeout,
				len(rows),
				describeRows(rows),
			)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
