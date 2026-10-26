package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// lastChangeAppRole is the base role of the application login. Migration 013
// grants it nothing on the audit schema.
const lastChangeAppRole = "app_auth"

// TestAuditLastChangeIgnoresUpdateRefusedForNonMember creates and renames an
// issue as a member of its organization. A user of another organization then
// tries to rename the issue through tack_update_issue, which refuses the
// request. The issue must keep the member's name, and `audit last-change`
// must report the member's rename as the known latest change of /name.
func TestAuditLastChangeIgnoresUpdateRefusedForNonMember(t *testing.T) {
	member := integration.NewAuditedMCPHarness(t)
	outsider := integration.NewAuditedMCPHarness(t)
	windowStart := clock.Now().Add(-time.Minute)
	issueID := createAuditedIssue(t, member, "access last change issue", nil)
	updateIssue(t, member, issueID, "access last change member rename", nil)
	refused := auditedIssueArguments(outsider)
	refused.NodeID = issueID.String()
	refused.Name = "access last change outsider rename"
	outsider.CallExpectError(t, "tack_update_issue", refused)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), nodeAuditTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	view := storedStoreView(t, stores, issueID)
	if view.Name != "access last change member rename" {
		t.Fatalf("issue %s name after the refused update = %q, want the member's rename", issueID, view.Name)
	}

	ledger := startNodeAuditLedger(t)
	updates := ledger.entityRows(t, view.OrgID, issueID, audit.VerbNodeUpdate, 1)
	rename := renameRow(t, updates, "access last change member rename")
	report := runLastChange(t, ledger, view.OrgID, issueID, windowStart, clock.Now().Add(time.Minute))
	requireField(t, report, "/name", audit.LastChangeKnown, rename, audit.LastChangeSet)
}

// TestAuditLastChangeRefusesWithoutAuditReaderRole runs `audit last-change`
// through the built server three times as different operators: without
// AUDIT_READER_DSN, with a login that inherits only the application role, and
// with a login that inherits audit_reader. The first two runs must exit with
// an error that states the missing setting or the permission refusal, and
// they must not print a report. The audit_reader run must print a report. The
// clispec choke-point records an audit host read before the read runs, as it
// does for `audit query` and `audit get`. The ledger must contain exactly one
// audit.read row with the ok outcome for each operator.
func TestAuditLastChangeRefusesWithoutAuditReaderRole(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	orgID, nodeID, now := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), clock.Now()
	cases := []struct {
		name      string
		readerDSN string
		refusal   string
	}{
		{name: "without AUDIT_READER_DSN", readerDSN: "", refusal: "AUDIT_READER_DSN required"},
		{name: "with the application role", readerDSN: ledgerLoginDSN(t, ledger, lastChangeAppRole), refusal: "permission denied"},
		{name: "with audit_reader", readerDSN: ledger.cfg.AuditReaderDSN, refusal: ""},
	}
	for _, current := range cases {
		operatorID := uuid.Must(uuid.NewV7())
		stdout, stderr, err := lastChangeCommand(t, current.readerDSN, operatorID, orgID, nodeID, now.Add(-time.Hour), now.Add(time.Hour))
		succeeds := current.refusal == ""
		printed := strings.Contains(stdout, `"audit.last_change"`)
		if (err == nil) != succeeds || printed != succeeds || !strings.Contains(stdout+stderr, current.refusal) {
			t.Fatalf("%s: error = %v, printed a report = %t, want success %t and output with %q\n%s%s",
				current.name, err, printed, succeeds, current.refusal, stdout, stderr)
		}
		want := operatorReadRow{Action: string(audit.VerbAuditRead), Outcome: string(audit.OutcomeOK), Code: ""}
		rows := waitForOperatorRows(t, ledger, operatorID, 1)
		if len(rows) != 1 || rows[0] != want {
			t.Fatalf("%s: ledger rows of operator %s = %+v, want only %+v", current.name, operatorID, rows, want)
		}
	}
}
