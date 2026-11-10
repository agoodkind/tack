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
		t.Fatalf(
			"issue %s name after the refused update = %q, want the member's rename",
			issueID,
			view.Name,
		)
	}

	ledger := startNodeAuditLedger(t)
	updates := ledger.entityRows(t, view.OrgID, issueID, audit.VerbNodeUpdate, 1)
	rename := renameRow(t, updates, "access last change member rename")
	report := runLastChange(
		t,
		ledger,
		view.OrgID,
		issueID,
		windowStart,
		clock.Now().Add(time.Minute),
	)
	requireField(t, report, "/name", audit.LastChangeKnown, rename, audit.LastChangeSet)
}

// TestAuditLastChangeRefusesWithoutAuditReaderRole runs audit last-change
// through the built server as four operators: one without AUDIT_READER_DSN,
// one with a login that inherits only the application role, one with a
// malformed AUDIT_READER_DSN, and one with a login that inherits audit_reader.
// The test checks that the first three runs exit with an error, log the missing
// setting, permission refusal, or fixed reader message, and do not print output.
// The audit_reader run prints a report. Each operator has a single final
// audit.read row of the read class in audit.events. The audit_reader row has
// outcome completed. Each refused run has outcome failed, code read_failed,
// and does not include read evidence. No run exposes the reader connection
// string.
func TestAuditLastChangeRefusesWithoutAuditReaderRole(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	orgID, nodeID, now := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), clock.Now()

	cases := []struct {
		name      string
		readerDSN string
		refusal   string
	}{
		{name: "without AUDIT_READER_DSN", readerDSN: "", refusal: "AUDIT_READER_DSN required"},
		{
			name:      "with the application role",
			readerDSN: ledgerLoginDSN(t, ledger, lastChangeAppRole),
			refusal:   "permission denied",
		},
		{name: "with audit_reader", readerDSN: ledger.cfg.AuditReaderDSN, refusal: ""},
		{
			name:      "with a malformed reader DSN",
			readerDSN: readerMalformedDSN,
			refusal:   readerFixedMessage,
		},
	}
	for _, current := range cases {
		operatorID := uuid.Must(uuid.NewV7())
		run := lastChangeCommand(
			t,
			current.readerDSN,
			operatorID,
			orgID,
			nodeID,
			now.Add(-time.Hour),
			now.Add(time.Hour),
		)
		succeeds := current.refusal == ""
		printed := strings.Contains(run.stdout, `"audit.last_change"`)
		if (run.err == nil) != succeeds || printed != succeeds ||
			!strings.Contains(run.log, current.refusal) {
			t.Fatalf(
				"%s: error = %v, printed a report = %t, want success %t and a log with %q\n%s",
				current.name,
				run.err,
				printed,
				succeeds,
				current.refusal,
				run.output(),
			)
		}

		if strings.Contains(run.output(), readerErrorSecret) ||
			strings.Contains(run.output(), readerParseErrorText) {
			t.Fatalf(
				"%s: the command exposed the reader connection string\n%s",
				current.name,
				run.output(),
			)
		}

		row := awaitReadLifecycleRow(t, ledger, operatorID, audit.VerbAuditRead)
		target := uriTarget(audit.NodeURIPrefix + nodeID.String())
		if succeeds {
			requireReadRowShape(t, row, audit.OutcomeCompleted, target)
		} else {
			if run.stdout != "" {
				t.Fatalf(
					"%s: stdout = %q, want no output from a refused read\n%s",
					current.name,
					run.stdout,
					run.output(),
				)
			}

			requireFailedReadRow(t, row, target, "")
		}
	}
}
