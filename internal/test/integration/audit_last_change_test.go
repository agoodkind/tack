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

// lastChangeLegacyGap separates the legacy row time from the changes before
// and after it.
const lastChangeLegacyGap = 50 * time.Millisecond

// TestAuditLastChangeReportsLatestChangePerPathAndRelationship changes one
// issue through the audited MCP harness. The production relay and the audit
// consumer write the ledger rows. The test renames the issue, sets its
// priority, renames it again, and removes its description. It adds and removes
// a relationship from the issue and adds a relationship to the issue. It
// records one legacy node.update row of the issue between the priority change
// and the second rename, and it makes one refused priority update. The built
// server then runs `audit last-change` as a login that inherits only
// audit_reader. The report must name the second rename for /name, the
// removal for /props/description, and the latest verb of each relationship.
// /props/priority must report unknown with its last known change, and the
// refused update must not appear as a change.
func TestAuditLastChangeReportsLatestChangePerPathAndRelationship(t *testing.T) {
	harness := integration.NewAuditedMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), nodeAuditTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	windowStart := clock.Now().Add(-time.Minute)
	createArguments := auditedIssueArguments(harness)
	createArguments.ProjectReference = harness.Project
	createArguments.Name = "last change issue"
	createArguments.Properties = datagen.NodeProperties{"description": quoted("first description")}
	issueID := uuid.MustParse(harness.Call(t, "tack_create_issue", createArguments).RawID())
	updateIssue(t, harness, issueID, "last change issue first rename", nil)
	updateIssue(t, harness, issueID, "", datagen.NodeProperties{"priority": quoted("high")})
	time.Sleep(lastChangeLegacyGap)
	legacyTime := clock.Now().UTC()
	time.Sleep(lastChangeLegacyGap)
	updateIssue(t, harness, issueID, "last change issue second rename", nil)
	updateIssue(t, harness, issueID, "", datagen.NodeProperties{"description": json.RawMessage("null")})

	otherArguments := auditedIssueArguments(harness)
	otherArguments.ProjectReference = harness.Project
	otherArguments.Name = "last change other issue"
	otherID := uuid.MustParse(harness.Call(t, "tack_create_issue", otherArguments).RawID())
	changeRelationship(t, harness, "tack_add_relationship", issueID, otherID)
	changeRelationship(t, harness, "tack_remove_relationship", issueID, otherID)
	changeRelationship(t, harness, "tack_add_relationship", otherID, issueID)
	refused := auditedIssueArguments(harness)
	refused.NodeID = issueID.String()
	refused.Properties = datagen.NodeProperties{"priority": quoted(refusedOption)}
	harness.CallExpectError(t, "tack_update_issue", refused)
	view, err := stores.Views.Get(t.Context(), issueID)
	if err != nil || view == nil {
		t.Fatalf("issue %s after the changes = %v, %v", issueID, view, err)
	}

	ledger := startNodeAuditLedger(t)
	legacyID := recordLegacyIssueUpdate(t, ledger, view.OrgID, issueID, legacyTime)
	rows := waitForLastChangeRows(t, ledger, view.OrgID, issueID, legacyID)
	report := runLastChange(t, ledger, view.OrgID, issueID, windowStart, clock.Now().Add(time.Minute))
	if report.Truncated || !report.HistoryStartInWindow {
		t.Fatalf("report truncated = %t, history start in window = %t; want a complete read", report.Truncated, report.HistoryStartInWindow)
	}

	firstRename := renameRow(t, rows.updates, "last change issue first rename")
	secondRename := renameRow(t, rows.updates, "last change issue second rename")
	name := requireField(t, report, "/name", audit.LastChangeKnown, secondRename, audit.LastChangeSet)
	if name.Change.CommitVersion <= commitVersion(t, firstRename) {
		t.Fatalf("/name commit version %q, want a version after the first rename %q", name.Change.CommitVersion, firstRename.Context.CommitVersion)
	}
	requireField(t, report, "/props/description", audit.LastChangeKnown, changedRow(t, rows.updates, "/props/description"), audit.LastChangeRemoved)
	priority := requireField(t, report, "/props/priority", audit.LastChangeUnknown, changedRow(t, rows.updates, "/props/priority"), audit.LastChangeSet)
	if len(priority.LegacyEventIDs) != 1 || priority.LegacyEventIDs[0] != legacyID {
		t.Fatalf("/props/priority legacy events = %v, want the legacy row %s", priority.LegacyEventIDs, legacyID)
	}

	removed := requireRelationship(t, report, issueID, otherID, audit.LastChangeRemoved)
	added := requireRelationship(t, report, otherID, issueID, audit.LastChangeSet)
	if removed.Change.Action != string(audit.VerbRelationshipRemove) || added.Change.Action != string(audit.VerbRelationshipAdd) {
		t.Fatalf("relationship actions = %s and %s, want remove and add", removed.Change.Action, added.Change.Action)
	}
	if removed.Change.EventID != rows.removal.EventID || added.Change.EventID != rows.reverseAdd.EventID {
		t.Fatalf("relationship events = %s and %s, want %s and %s", removed.Change.EventID, added.Change.EventID, rows.removal.EventID, rows.reverseAdd.EventID)
	}
	requireRefusalUncounted(t, report, rows.refused)
}

// updateIssue renames the issue when name is not empty and sets properties.
func updateIssue(t *testing.T, harness *integration.MCPHarness, issueID uuid.UUID, name string, properties datagen.NodeProperties) {
	t.Helper()
	arguments := auditedIssueArguments(harness)
	arguments.NodeID = issueID.String()
	arguments.Name = name
	arguments.Properties = properties
	harness.Call(t, "tack_update_issue", arguments)
}

// changeRelationship calls tool for the blocks relationship from sourceID to
// targetID.
func changeRelationship(t *testing.T, harness *integration.MCPHarness, tool string, sourceID, targetID uuid.UUID) {
	t.Helper()
	arguments := auditedIssueArguments(harness)
	arguments.SourceID = sourceID.String()
	arguments.RelationType = lastChangeRelationType
	arguments.TargetID = targetID.String()
	harness.Call(t, tool, arguments)
}
