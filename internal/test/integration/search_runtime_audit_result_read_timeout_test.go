//go:build runtimeaudit

package integration_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	resultReadPageMessage   = "search.page.returned"
	resultReadFailedMessage = "search.result_read_failed"
)

// TestSearchRuntimeAuditResultReadTimeoutReturnsUnavailable adds enough nodes
// to leave a page unread after a cursor continuation. It pauses FoundationDB
// after search.page.returned and resumes it after search.result_read_failed.
// It checks the temporarily unavailable response and the failed ledger read
// row with code search_unavailable. It retries the same cursor and checks the
// stored replay results without changing any stored session record.
func TestSearchRuntimeAuditResultReadTimeoutReturnsUnavailable(t *testing.T) {
	fixture := newFailureFixture(t)
	putIdleCorpus(t, fixture.QueryFixture, failureAuditQuery, sessionAuditNodes)
	opened := integration.CallSearch(t, fixture.Harness, failureAuditQuery, "")
	if opened.Cursor == "" {
		t.Fatal("the first page did not return a cursor")
	}
	before := integration.CursorSession(t, fixture.QueryFixture, opened.Cursor).Version
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	hooks := installRetireLogHooks(t, "", map[string]func(){
		resultReadPageMessage:   func() { setContainerPaused(t, container, true) },
		resultReadFailedMessage: func() { setContainerPaused(t, container, false) },
	})

	_, err := integration.TrySearch(fixture.Harness, failureAuditQuery, opened.Cursor)
	hooks.requireFinished(t)

	if err == nil || err.Error() != sessionTimeoutUnavailableText {
		t.Fatalf("search returned error %v; expected text %q", err, sessionTimeoutUnavailableText)
	}
	committed := integration.CursorSession(t, fixture.QueryFixture, opened.Cursor)
	if committed.Version != before+1 || committed.Complete {
		t.Fatalf("the session after the timeout has version %d and complete %t, want version %d and unread pages", committed.Version, committed.Complete, before+1)
	}
	store := fixture.Stores.SearchSessions(clock.Wall{}, failureAuditIdle)
	stored, found, err := store.Replay(integration.WithTestOperation(t, t.Context()), committed.ID, before)
	if err != nil || !found {
		t.Fatalf("read the replay record at version %d: found %t, error %v", before, found, err)
	}
	records := sessionRecords(t, committed.ID)
	replayed, err := integration.TrySearch(fixture.Harness, failureAuditQuery, opened.Cursor)
	if err != nil || !slices.Equal(replayed.IDs, stored.ResultIDs) {
		t.Fatalf("the retry with the same cursor returned %v and error %v, want the replay record %v", replayed.IDs, err, stored.ResultIDs)
	}
	requireSessionUnchanged(t, "the retry after the timeout", records, sessionRecords(t, committed.ID))
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(current []audit.Row) bool {
		return len(cleanupLossReads(current, audit.OutcomeFailed)) == 1
	})
	failed := onlyRow(t, cleanupLossReads(rows, audit.OutcomeFailed), "the failed read")
	if failed.Error == nil || failed.Error.Code != searchUnavailableCode {
		t.Fatalf("the failed read event did not store the %s code: %+v", searchUnavailableCode, failed)
	}
}

// TestSearchRuntimeAuditAssigneeReadTimeoutWrapsStorageTimeout pauses
// FoundationDB and checks that ListBySources returns an error matching
// domain.ErrStorageTimeout.
func TestSearchRuntimeAuditAssigneeReadTimeoutWrapsStorageTimeout(t *testing.T) {
	cluster := testenv.FoundationDB(t)
	stores, err := fdbadapter.NewStores(cluster, integration.StoreTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open FoundationDB stores: %v", err)
	}
	container := testenv.FoundationDBContainer(cluster)
	setContainerPaused(t, container, true)
	_, err = stores.Relationships.ListBySources(t.Context(), uuid.Must(uuid.NewV7()), []uuid.UUID{uuid.Must(uuid.NewV7())}, node.RelAssignedTo)
	setContainerPaused(t, container, false)
	if !errors.Is(err, domain.ErrStorageTimeout) {
		t.Fatalf("ListBySources with a paused FoundationDB returned error %v, want a match for domain.ErrStorageTimeout", err)
	}
}
