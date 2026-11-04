//go:build runtimeaudit

package integration_test

import (
	"testing"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// commitDeadline is the query deadline of the commit deadline test.
	commitDeadline = 15 * time.Second
	// commitDeadlineSummaryRead is the log message after the node summary read.
	// This is the last read before the page commit.
	commitDeadlineSummaryRead = "store.node_summary.read"
)

// TestSearchRuntimeAuditCommitDeadlineReturnsUnavailable sets the query
// deadline to 15 seconds and opens a session over 60 nodes. A log hook waits
// past the deadline during the continuation's node summary read. This is the
// last read before the page commit. The transaction ends with
// context.DeadlineExceeded before it submits a commit. The test checks the
// temporarily unavailable response and a failed read row with code
// search_unavailable. It checks that the session records remain unchanged.
// It retries the same cursor and requires a page. After the audit consumer
// stores every record of the topic, the test checks that the ledger does not
// store an incomplete session write event for the read.
func TestSearchRuntimeAuditCommitDeadlineReturnsUnavailable(t *testing.T) {
	t.Setenv("OPENSEARCH_QUERY_DEADLINE", commitDeadline.String())
	fixture := newFailureFixture(t)
	putIdleCorpus(t, fixture.QueryFixture, failureAuditQuery, sessionAuditNodes)
	opened := integration.CallSearch(t, fixture.Harness, failureAuditQuery, "")
	if opened.Cursor == "" {
		t.Fatal("the first page did not return a cursor")
	}
	session := integration.CursorSession(t, fixture.QueryFixture, opened.Cursor)
	records := sessionRecords(t, session.ID)
	hooks := installRetireLogHooks(t, "", map[string]func(){
		commitDeadlineSummaryRead: func() { time.Sleep(commitDeadline) },
	})

	_, err := integration.TrySearch(fixture.Harness, failureAuditQuery, opened.Cursor)
	hooks.requireFinished(t)

	if err == nil || err.Error() != sessionTimeoutUnavailableText {
		t.Fatalf("search returned error %v; want text %q", err, sessionTimeoutUnavailableText)
	}
	requireSessionUnchanged(t, "the search past its deadline", records, sessionRecords(t, session.ID))
	retried, err := integration.TrySearch(fixture.Harness, failureAuditQuery, opened.Cursor)
	if err != nil || len(retried.IDs) == 0 {
		t.Fatalf("the retry with the same cursor returned %d results and error %v, want a page", len(retried.IDs), err)
	}
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(current []audit.Row) bool {
		return len(cleanupLossReads(current, audit.OutcomeFailed)) == 1
	})
	failed := onlyRow(t, cleanupLossReads(rows, audit.OutcomeFailed), "the failed read")
	if failed.Error == nil || failed.Error.Code != searchUnavailableCode {
		t.Fatalf("the failed read event did not store the %s code: %+v", searchUnavailableCode, failed)
	}
	waitForTopicConsumed(t, fixture.audited.ledger)
	rows = fixture.audited.ledgerRows(t, fixture.Workspaces[0].OrgID)
	if events := writeUncertainEvents(rows, failed.Context.OperationID); len(events) != 0 {
		t.Fatalf("stored %d incomplete session write events for operation %s; want 0", len(events), failed.Context.OperationID)
	}
}

// waitForTopicConsumed waits until the ledger topic's consumer group has
// stored every record in the topic.
func waitForTopicConsumed(t *testing.T, ledger publicLedger) {
	t.Helper()
	reader, topic := ledger.stack.reader, ledger.stack.topic
	waitFor(t, "consumption of every record", func() bool {
		offsets, err := reader.ConsumerOffsets(t.Context())
		if err != nil {
			t.Fatalf("read the consumer offsets: %v", err)
		}
		for _, offset := range offsets {
			if offset.Topic == topic {
				return topicConsumed(t, reader, testenv.Kafka(t), topic, offset.Group)
			}
		}
		return false
	})
}
