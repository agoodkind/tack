//go:build runtimeaudit

package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	resultReadPageMessage   = "search.page.returned"
	resultReadFailedMessage = "search.result_read_failed"
)

// TestSearchRuntimeAuditResultReadTimeoutReturnsUnavailable pauses FoundationDB
// after search.page.returned and resumes it after search.result_read_failed.
// It requires the temporarily unavailable response from tack_search and a
// failed ledger read row with code search_unavailable.
func TestSearchRuntimeAuditResultReadTimeoutReturnsUnavailable(t *testing.T) {
	fixture := newFailureFixture(t)
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	hooks := installRetireLogHooks(t, "", map[string]func(){
		resultReadPageMessage:   func() { setContainerPaused(t, container, true) },
		resultReadFailedMessage: func() { setContainerPaused(t, container, false) },
	})

	_, err := integration.TrySearch(fixture.Harness, failureAuditQuery, "")
	hooks.requireFinished(t)

	if err == nil || err.Error() != sessionTimeoutUnavailableText {
		t.Fatalf("search returned error %v; expected text %q", err, sessionTimeoutUnavailableText)
	}
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(current []audit.Row) bool {
		return len(cleanupLossReads(current, audit.OutcomeFailed)) == 1
	})
	failed := onlyRow(t, cleanupLossReads(rows, audit.OutcomeFailed), "the failed read")
	if failed.Error == nil || failed.Error.Code != searchUnavailableCode {
		t.Fatalf("the failed read event did not store the %s code: %+v", searchUnavailableCode, failed)
	}
}
