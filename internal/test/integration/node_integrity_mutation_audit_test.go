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

// TestNodeIntegrityMutationSetterStagesAuditEntity checks the stored node,
// its read row, and the node.update outbox event after an issue state change.
// It uses real FoundationDB.
func TestNodeIntegrityMutationSetterStagesAuditEntity(t *testing.T) {
	harness := integration.NewAuditedMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), schemaTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	issueID, reference := createSchemaIssue(t, harness, "Setter carrier")
	harness.SetIssueState(t, reference, "Done")

	view := storedStoreView(t, stores, issueID)
	stored, err := stores.Nodes.Get(t.Context(), view.OrgID, issueID)
	if err != nil || stored == nil {
		t.Fatalf("get issue %s: %v, %v", issueID, stored, err)
	}
	doneState := string(quoted(schemaStateID(t, stores, view, "Done").String()))
	if string(stored.Props["state_id"]) != doneState || string(view.Props["state_id"]) != doneState {
		t.Fatalf("issue %s state_id = %s in the node and %s in the read row, want %s",
			issueID, stored.Props["state_id"], view.Props["state_id"], doneState)
	}
	var event audit.Event
	if err := json.Unmarshal(outboxEvent(t, stores, issueID, audit.VerbNodeUpdate), &event); err != nil {
		t.Fatalf("decode the node.update event of %s: %v", issueID, err)
	}
	entity := event.Entity
	if entity.Identifier != reference || entity.NodeType != "issue" || entity.Name != stored.Name || event.Actor.ID != stored.UpdatedBy {
		t.Fatalf("node.update entity %+v and actor %s, want reference %s, type issue, name %q, and actor %s",
			entity, event.Actor.ID, reference, stored.Name, stored.UpdatedBy)
	}
}
