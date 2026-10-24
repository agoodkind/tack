package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// deliveryDelay is the wait between the last mutation and the start of the
// relay and consumer.
const deliveryDelay = 2 * time.Second

// TestNodeIntegrityAuditKeepsMutationTime creates and renames an issue
// through MCP, waits deliveryDelay, and then starts the production relay and
// audit consumer. Each ledger row must have an event time inside the window
// of the MCP calls and before the relay and consumer started. Each row's
// completion time must equal its event time.
func TestNodeIntegrityAuditKeepsMutationTime(t *testing.T) {
	harness := integration.NewAuditedMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), nodeAuditTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	windowStart := clock.Now().UTC().Truncate(time.Microsecond)
	issueID, _ := createSchemaIssue(t, harness, "timed issue")
	rename := schemaArguments(harness)
	rename.ProjectReference = ""
	rename.NodeID = issueID.String()
	rename.Name = "timed issue renamed"
	harness.Call(t, "tack_update_issue", rename)
	windowEnd := clock.Now().UTC()
	view := storedStoreView(t, stores, issueID)

	time.Sleep(deliveryDelay)
	deliveryStart := clock.Now().UTC()
	ledger := startNodeAuditLedger(t)
	rows := append(
		ledger.entityRows(t, view.OrgID, issueID, audit.VerbNodeCreate, 1),
		ledger.entityRows(t, view.OrgID, issueID, audit.VerbNodeUpdate, 1)...,
	)
	for _, row := range rows {
		eventTime := row.EventTime.UTC()
		if eventTime.Before(windowStart) || eventTime.After(windowEnd) || !eventTime.Before(deliveryStart) {
			t.Fatalf("%s row %s event time = %s, want a time from %s to %s, before delivery started at %s",
				row.Action, row.EventID, eventTime, windowStart, windowEnd, deliveryStart)
		}
		if !row.Context.CompletedAt.UTC().Truncate(time.Microsecond).Equal(eventTime) {
			t.Fatalf("%s row %s completed at %s, want its event time %s", row.Action, row.EventID, row.Context.CompletedAt, eventTime)
		}
	}
}

// TestNodeIntegrityAuditRefusedUpdateRecordsNoChange creates an issue through
// MCP. Schema validation then refuses a tack_update_issue request with an
// undeclared priority option. The FoundationDB outbox must not contain a
// change event for the issue after the refused request. After the relay and
// consumer deliver the create event and report zero lag, the ledger must not
// contain a change row that updates the issue.
func TestNodeIntegrityAuditRefusedUpdateRecordsNoChange(t *testing.T) {
	harness := integration.NewAuditedMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), nodeAuditTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	issueID, _ := createSchemaIssue(t, harness, "refused update issue")
	view := storedStoreView(t, stores, issueID)
	mark := storeOutboxTail(t, stores)
	refused := schemaArguments(harness)
	refused.ProjectReference = ""
	refused.NodeID = issueID.String()
	refused.Properties = datagen.NodeProperties{"priority": quoted(refusedOption)}
	harness.CallExpectError(t, "tack_update_issue", refused)
	if changes := issueChangeEvents(t, stores, mark, issueID); len(changes) != 0 {
		t.Fatalf("the refused update wrote change events %+v", changes)
	}

	ledger := startNodeAuditLedger(t)
	ledger.entityRows(t, view.OrgID, issueID, audit.VerbNodeCreate, 1)
	ledger.waitForNoLag(t)
	page, err := ledger.reader.QueryPage(t.Context(), audit.QueryFilter{
		OrgID: view.OrgID, Oldest: clock.Now().Add(-time.Hour), Latest: clock.Now().Add(time.Hour),
		Action: string(audit.VerbNodeUpdate), ActorID: uuid.Nil, EntityID: issueID, RequestID: "", TraceID: "", Limit: 0,
	}, "")
	if err != nil {
		t.Fatalf("query the update rows of issue %s: %v", issueID, err)
	}
	for _, row := range page.Rows {
		if row.Context.Phase == audit.PhaseChange || row.Delta != nil {
			t.Fatalf("the ledger recorded the refused update of issue %s as change row %+v", issueID, row)
		}
	}
}

// storeOutboxTail returns the mark of the last FoundationDB outbox entry, or
// nil when the outbox is empty.
func storeOutboxTail(t *testing.T, stores *fdbadapter.Stores) []byte {
	t.Helper()
	var mark []byte
	for {
		entries, err := stores.OpsOutbox.ReadOutboxFrom(t.Context(), mark, outboxPageLimit)
		if err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		if len(entries) == 0 {
			return mark
		}
		mark = entries[len(entries)-1].Mark
	}
}

// issueChangeEvents returns the change events for issueID in the
// FoundationDB outbox after mark. It skips entries that do not decode as one
// audit event.
func issueChangeEvents(t *testing.T, stores *fdbadapter.Stores, mark []byte, issueID uuid.UUID) []audit.Event {
	t.Helper()
	changes := []audit.Event{}
	for {
		entries, err := stores.OpsOutbox.ReadOutboxFrom(t.Context(), mark, outboxPageLimit)
		if err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		if len(entries) == 0 {
			return changes
		}
		for _, entry := range entries {
			var event audit.Event
			if json.Unmarshal(entry.Event, &event) != nil || event.Entity.ID != issueID {
				continue
			}
			if event.Context.Phase == audit.PhaseChange || event.Delta != nil {
				changes = append(changes, event)
			}
		}
		mark = entries[len(entries)-1].Mark
	}
}
