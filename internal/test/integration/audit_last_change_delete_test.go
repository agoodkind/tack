package integration_test

import (
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

// TestAuditLastChangeReportsDeletionOfFieldsAndRelationships creates an issue
// with a description through the audited MCP harness, renames it, adds a
// blocks relationship from the issue and one to the issue, and deletes the
// issue with tack_delete_issue. `audit last-change` for the deleted issue
// must report the creation in the window and, for every field path, the
// node.delete change as known with a removal. Each blocks relationship must
// report as known the relationship.remove change that the delete operation
// wrote.
func TestAuditLastChangeReportsDeletionOfFieldsAndRelationships(t *testing.T) {
	harness := integration.NewAuditedMCPHarness(t)
	windowStart := clock.Now().Add(-time.Minute)
	issueID := createAuditedIssue(t, harness, "deleted last change issue", datagen.NodeProperties{"description": quoted("deleted description")})
	otherID := createAuditedIssue(t, harness, "deleted last change other issue", nil)
	updateIssue(t, harness, issueID, "deleted last change issue renamed", nil)
	changeRelationship(t, harness, "tack_add_relationship", issueID, otherID)
	changeRelationship(t, harness, "tack_add_relationship", otherID, issueID)
	remove := auditedIssueArguments(harness)
	remove.NodeID = issueID.String()
	harness.Call(t, "tack_delete_issue", remove)

	orgID := nodeOrgID(t, otherID)
	ledger := startNodeAuditLedger(t)
	deletion := ledger.entityRows(t, orgID, issueID, audit.VerbNodeDelete, 1)[0]
	outgoing := waitForRemoval(t, ledger, orgID, issueID, otherID)
	incoming := waitForRemoval(t, ledger, orgID, otherID, issueID)
	report := runLastChange(t, ledger, orgID, issueID, windowStart, clock.Now().Add(time.Minute))
	if report.Truncated || !report.HistoryStartInWindow || len(report.Fields) == 0 {
		t.Fatalf("report truncated = %t, history start = %t, fields = %d; want a complete read with fields",
			report.Truncated, report.HistoryStartInWindow, len(report.Fields))
	}
	for _, entry := range report.Fields {
		requireChange(t, entry.Path, entry, audit.LastChangeKnown, deletion, audit.LastChangeRemoved)
	}
	for _, removal := range []audit.Row{outgoing, incoming} {
		if removal.Context.OperationID != deletion.Context.OperationID {
			t.Fatalf("removal %s operation = %s, want the delete operation %s", removal.EventID, removal.Context.OperationID, deletion.Context.OperationID)
		}
		target := removal.Context.Target
		entry := requireRelationship(t, report, target.SourceID, target.TargetID, audit.LastChangeKnown, audit.LastChangeRemoved)
		if entry.Change.EventID != removal.EventID || entry.Change.Action != string(audit.VerbRelationshipRemove) {
			t.Fatalf("relationship %s to %s change = %+v, want removal %s", target.SourceID, target.TargetID, entry.Change, removal.EventID)
		}
	}
}

// createAuditedIssue creates an issue in the harness project with name and
// properties and returns its ID.
func createAuditedIssue(t *testing.T, harness *integration.MCPHarness, name string, properties datagen.NodeProperties) uuid.UUID {
	t.Helper()
	arguments := auditedIssueArguments(harness)
	arguments.ProjectReference = harness.Project
	arguments.Name = name
	arguments.Properties = properties
	return uuid.MustParse(harness.Call(t, "tack_create_issue", arguments).RawID())
}

// nodeOrgID returns the organization of the stored node nodeID.
func nodeOrgID(t *testing.T, nodeID uuid.UUID) uuid.UUID {
	t.Helper()
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), nodeAuditTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	view, err := stores.Views.Get(t.Context(), nodeID)
	if err != nil || view == nil {
		t.Fatalf("node %s = %v, %v", nodeID, view, err)
	}
	return view.OrgID
}

// waitForRemoval polls the ledger until it contains the relationship.remove
// change row of the blocks relationship from sourceID to targetID.
func waitForRemoval(t *testing.T, ledger *nodeAuditLedger, orgID, sourceID, targetID uuid.UUID) audit.Row {
	t.Helper()
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		for _, row := range ledger.orgRows(t, orgID, uuid.Nil, audit.VerbRelationshipRemove) {
			if relationshipChange(row) && row.Context.Target.SourceID == sourceID && row.Context.Target.TargetID == targetID {
				return row
			}
		}
		if clock.Now().After(deadline) {
			t.Fatalf("no relationship.remove row from %s to %s after %s", sourceID, targetID, nodeAuditDeadline)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}
