package integration_test

import (
	"encoding/json"
	"testing"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// TestNodeIntegrityMutationUpdateStagesAuditEntity tests an issue state update
// through tack_update_issue by the printed state name with the audit writer
// enabled. The update must store the Done state. The outbox must record
// exactly one node.update event for the issue with the issue reference, the
// issue node type, and the calling user as actor.
func TestNodeIntegrityMutationUpdateStagesAuditEntity(t *testing.T) {
	harness := integration.NewAuditedMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), schemaTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	issueID, reference := createSchemaIssue(t, harness, "Audited state")
	updateSchemaState(t, harness, issueID, quoted("Done"))

	issue := storedStoreView(t, stores, issueID)
	doneID := schemaStateID(t, stores, issue, "Done")
	if got := string(issue.Props["state_id"]); got != string(quoted(doneID.String())) {
		t.Fatalf("issue %s state_id = %s, want the Done state %s", issueID, got, doneID)
	}
	var event audit.Event
	if err := json.Unmarshal(outboxEvent(t, stores, issueID, audit.VerbNodeUpdate), &event); err != nil {
		t.Fatalf("decode the node.update event of %s: %v", issueID, err)
	}
	if event.Entity.Identifier != reference || event.Entity.NodeType != "issue" || event.Actor.ID != issue.UpdatedBy {
		t.Fatalf("node.update entity %+v and actor %s, want reference %s, type issue, and actor %s",
			event.Entity, event.Actor.ID, reference, issue.UpdatedBy)
	}
}
