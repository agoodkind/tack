package ops

import (
	"strings"
	"testing"
	"time"
)

// TestBackupAlarmReportsUnreadableEvidence checks refused store and master
// connections without claiming that success records do not exist. The email
// explains store failure once and supplies each mechanism's recovery steps
// without client error text.
func TestBackupAlarmReportsUnreadableEvidence(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")

	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("three faults on one run must produce one email, sent %d", len(captured.messages))
	}

	wantSubject := "[" + backupAlarmHost() + "] 3 backup problems"
	if captured.messages[0].Subject != wantSubject {
		t.Errorf("subject = %q, want %q", captured.messages[0].Subject, wantSubject)
	}

	body := captured.messages[0].Body

	for _, sentence := range []string{
		"The object store (which stores the backups) did not answer this check. " +
			"Confirm the object store guest is running before the steps below.\n\n" +
			"This guest could not read nightly ledger export status\n" +
			"This guest could not date the nightly ledger export's newest copy.\n" +
			"1. On the owner guest, run journalctl -u tack-ledger-export.\n",
		"\n\nThis guest could not read restore rehearsal status\n" +
			"This guest could not read the restore rehearsal's last pass.\n" +
			"1. On the owner guest, run journalctl -u tack-backup-restore-drill.\n",
		"\n\nThis guest cannot read ledger cluster health\n" +
			"This guest could not read a usable health report from the ledger cluster (logins and audit trail). " +
			"This check cannot establish whether the cluster is healthy.\n" +
			"1. Confirm every ledger guest is up.\n",
	} {
		if !strings.Contains(body, sentence) {
			t.Errorf("body is missing %q:\n%s", sentence, body)
		}
	}

	if !strings.HasPrefix(body, "The object store") {
		t.Errorf("the body must explain the object store first:\n%s", body)
	}

	for _, claim := range []string{
		"has never",
		"never passed",
		"never been seen",
		"reported",
		"failed",
	} {
		if strings.Contains(body, claim) {
			t.Errorf("unreadable evidence must not imply absent records (%q):\n%s", claim, body)
		}
	}

	assertBackupAlarmPlainWords(t, captured.messages[0].Subject, body, cfg.BackupS3Endpoint)
}

// TestBackupAlarmReportsMissingSuccessRecords checks distinct wording for
// readable absent export and rehearsal records, while unanswered master probes
// still cannot establish cluster health.
func TestBackupAlarmReportsMissingSuccessRecords(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := storedBackupStalenessConfig(t, newBackupTestStore(t, nil))

	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("three faults on one run must produce one email, sent %d", len(captured.messages))
	}

	body := captured.messages[0].Body

	for _, sentence := range []string{
		"Nightly ledger export has never completed\n" +
			"The nightly ledger export (the daily copy of the ledger in the object store) has never completed. " +
			"The last check reported: no complete export run in " + cfg.BackupS3BucketMain + ".\n" +
			"1. On the owner guest, run journalctl -u tack-ledger-export.\n",
		"\n\nRestore rehearsal has never passed\n" +
			"The restore rehearsal (the daily test restore) has never passed. " +
			"The last check reported: no backup-status/rehearsal.json in " + cfg.BackupS3BucketMain + ".\n" +
			"1. On the owner guest, run journalctl -u tack-backup-restore-drill.\n",
		// Shared-record absence cannot replace this guest's own cluster observation.
		"\n\nThis guest cannot read ledger cluster health\n" +
			"This guest could not read a usable health report from the ledger cluster (logins and audit trail). " +
			"This check cannot establish whether the cluster is healthy.\n",
		"\n1. Confirm every ledger guest is up.\n",
	} {
		if !strings.Contains(body, sentence) {
			t.Errorf("body is missing %q:\n%s", sentence, body)
		}
	}

	if strings.Contains(body, "This guest could not date the nightly ledger export") ||
		strings.Contains(body, "This guest could not read the restore rehearsal's last pass") {
		t.Errorf("readable empty store records must not use unreadable-record wording:\n%s", body)
	}

	assertBackupAlarmPlainWords(t, captured.messages[0].Subject, body, cfg.BackupS3Endpoint)
}
