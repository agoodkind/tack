package integration_test

import (
	"testing"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// commitVersionLength is the number of hexadecimal characters in a 10-byte
// transaction version followed by a 2-byte user version.
const commitVersionLength = 24

// TestNodeIntegrityAuditCommitVersionOrder creates an issue through the MCP
// handler and renames it twice. The production relay publishes the events to
// Kafka, and the audit consumer writes them to the ledger. The test reads
// the rows through Reader.QueryPage, which the audit query command uses.
// It checks that each commit version has 24 characters and that the values
// increase in create and rename order. It then exports the organization's
// ledger and verifies the hash chains.
func TestNodeIntegrityAuditCommitVersionOrder(t *testing.T) {
	harness := integration.NewAuditedMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), nodeAuditTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	createArguments := auditedIssueArguments(harness)
	createArguments.ProjectReference = harness.Project
	createArguments.Name = "ordered issue"
	issueID := uuid.MustParse(harness.Call(t, "tack_create_issue", createArguments).RawID())
	renames := []string{"ordered issue first rename", "ordered issue second rename"}
	for _, name := range renames {
		updateArguments := auditedIssueArguments(harness)
		updateArguments.NodeID = issueID.String()
		updateArguments.Name = name
		harness.Call(t, "tack_update_issue", updateArguments)
	}
	view, err := stores.Views.Get(t.Context(), issueID)
	if err != nil || view == nil || view.Name != renames[1] {
		t.Fatalf("issue %s after the renames = %+v, %v, want the name %q", issueID, view, err, renames[1])
	}

	ledger := startNodeAuditLedger(t)
	created := ledger.entityRows(t, view.OrgID, issueID, audit.VerbNodeCreate, 1)
	updated := ledger.entityRows(t, view.OrgID, issueID, audit.VerbNodeUpdate, len(renames))
	versions := []string{commitVersion(t, created[0])}
	for _, name := range renames {
		versions = append(versions, commitVersion(t, renamedRow(t, updated, name)))
	}
	for position := 1; position < len(versions); position++ {
		if versions[position-1] >= versions[position] {
			t.Fatalf("commit versions in commit order = %v, want strictly increasing values", versions)
		}
	}
	ledger.requireChainVerifies(t, view.OrgID)
}

// commitVersion requires row.Context.CommitVersion to have
// commitVersionLength characters, then returns that value.
func commitVersion(t *testing.T, row audit.Row) string {
	t.Helper()
	if len(row.Context.CommitVersion) != commitVersionLength {
		t.Fatalf("%s row %s commit version = %q, want %d hexadecimal characters", row.Action, row.EventID, row.Context.CommitVersion, commitVersionLength)
	}
	return row.Context.CommitVersion
}

// renamedRow returns the update row with name as the new name in its delta.
func renamedRow(t *testing.T, rows []audit.Row, name string) audit.Row {
	t.Helper()
	for _, row := range rows {
		if row.Delta == nil {
			continue
		}
		after := decodeAuditedIssueFields(t, row.Delta.After)
		if after.Name != nil && *after.Name == name {
			return row
		}
	}
	t.Fatalf("update rows %+v do not contain the rename to %q", rows, name)
	var missing audit.Row
	return missing
}
