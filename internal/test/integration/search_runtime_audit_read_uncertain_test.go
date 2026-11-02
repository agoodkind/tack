//go:build runtimeaudit

package integration_test

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	sessionCreateCommitReady = "search.session.create_commit_ready"
	sessionPageCommitReady   = "search.session.page_commit_ready"
	snapshotStepRecorded     = "audit.external.recorded"
	commitUncertainCode      = "search_commit_uncertain"
	searchUnavailableCode    = "search_unavailable"
)

// TestSearchRuntimeAuditCommitUncertainReadOutcome calls tack_search three
// times. For the session create commit and the page commit, a log hook pauses
// the test FoundationDB container after the transaction prepares its writes.
// Each commit times out with error 1031. In the third case, the hook pauses the
// container before the session create transaction reads, and the transaction
// ends without a commit. A second hook resumes the container at the storage
// failure log. Each call must return the fixed unavailable text and an empty
// page. The two uncertain commits must store a read row with the incomplete
// outcome and code search_commit_uncertain. The third case must store the
// failed outcome and code search_unavailable.
func TestSearchRuntimeAuditCommitUncertainReadOutcome(t *testing.T) {
	fixture := newFailureFixture(t)
	harness, _ := newCaptureHarness(t, fixture.QueryFixture, false)
	before := listOpenPITs(t, fixture)
	t.Cleanup(func() {
		for _, identifier := range addedPITs(listOpenPITs(t, fixture), before) {
			deleteObservedSnapshot(t, fixture, identifier)
		}
	})
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	cases := []struct {
		name    string
		pause   string
		outcome audit.Outcome
		code    string
	}{
		{name: "create commit", pause: sessionCreateCommitReady, outcome: audit.OutcomeIncomplete, code: commitUncertainCode},
		{name: "page commit", pause: sessionPageCommitReady, outcome: audit.OutcomeIncomplete, code: commitUncertainCode},
		{name: "no commit", pause: snapshotStepRecorded, outcome: audit.OutcomeFailed, code: searchUnavailableCode},
	}
	stored := map[string]int{}
	for _, current := range cases {
		t.Run(current.name, func(t *testing.T) {
			hooks := installRetireLogHooks(t, "", map[string]func(){
				current.pause:       func() { setContainerPaused(t, container, true) },
				retireStorageFailed: func() { setContainerPaused(t, container, false) },
			})
			page, err := integration.TrySearch(harness, failureAuditQuery, "")
			hooks.requireFinished(t)
			if err == nil || err.Error() != sessionTimeoutUnavailableText || len(page.IDs) != 0 || page.Cursor != "" {
				t.Fatalf("tack_search returned error %v, result count %d, cursor %q; want text %q", err, len(page.IDs), page.Cursor, sessionTimeoutUnavailableText)
			}
			stored[current.code]++
			rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(found []audit.Row) bool {
				return len(readsWithCode(found, current.outcome, current.code)) == stored[current.code]
			})
			matching := readsWithCode(rows, current.outcome, current.code)
			if len(matching) != stored[current.code] {
				t.Fatalf("read row = %+v; want outcome %s and code %s", rows, current.outcome, current.code)
			}
			for _, row := range matching {
				if strings.Contains(row.Error.Message, transactionTimedOutText) {
					t.Fatalf("read row message %q contains raw database error %q", row.Error.Message, transactionTimedOutText)
				}
			}
		})
	}
}

func readsWithCode(rows []audit.Row, outcome audit.Outcome, code string) []audit.Row {
	selected := []audit.Row{}
	for _, row := range cleanupLossReads(rows, outcome) {
		if row.Error != nil && row.Error.Code == code {
			selected = append(selected, row)
		}
	}
	return selected
}
