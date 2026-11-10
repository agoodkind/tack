package ops

import (
	"strings"
	"testing"
	"time"
)

// TestBackupStalenessAlarmUnreadableWords runs the command with an object
// store and ledger masters that refuse connections. The guest has not read the
// store before. These refusals establish unreadable records rather than absent
// backups. The message must report that distinction, identify the store once
// before the faults, and put each fault's steps below its phrase without client
// error text.
func TestBackupStalenessAlarmUnreadableWords(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")

	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("three faults on one run must mail once, sent %d", len(captured.messages))
	}
	wantSubject := "[" + backupAlarmHost() + "] 3 backup problems"
	if captured.messages[0].Subject != wantSubject {
		t.Errorf("subject = %q, want %q", captured.messages[0].Subject, wantSubject)
	}
	body := captured.messages[0].Body
	for _, sentence := range []string{
		"The object store (where the backups are kept) did not answer this check. " +
			"Confirm the object store guest is running before the steps below.\n\n" +
			"Nightly ledger export status could not be read\n" +
			"The nightly ledger export's newest copy could not be dated.\n" +
			"1. On the owner guest, run journalctl -u tack-ledger-export.\n",
		"\n\nRestore rehearsal status could not be read\n" +
			"The restore rehearsal's last pass could not be read.\n" +
			"1. On the owner guest, run journalctl -u tack-backup-restore-drill.\n",
		"\n\nThis guest cannot see the ledger cluster\n" +
			"This guest cannot reach the ledger cluster (logins and audit trail), " +
			"so it cannot say whether the cluster is healthy.\n" +
			"1. Confirm every ledger guest is up.\n",
	} {
		if !strings.Contains(body, sentence) {
			t.Errorf("body is missing %q:\n%s", sentence, body)
		}
	}
	if !strings.HasPrefix(body, "The object store") {
		t.Errorf("the body must open on the object store, named once:\n%s", body)
	}
	for _, claim := range []string{"has never", "never passed", "never been seen", "reported", "failed"} {
		if strings.Contains(body, claim) {
			t.Errorf("an unreadable store must not be described as empty (%q):\n%s", claim, body)
		}
	}
	assertBackupAlarmPlainWords(t, captured.messages[0].Subject, body, cfg.BackupS3Endpoint)
}

// TestBackupStalenessAlarmNeverRecordedWords runs the command against a
// readable store without an export run or success marker. The message reports
// that the store has not recorded a success, uses different words from the
// unreadable case, and puts each fault's steps below its phrase.
func TestBackupStalenessAlarmNeverRecordedWords(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := storedBackupStalenessConfig(t, newBackupTestStore(t, nil))

	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("three faults on one run must mail once, sent %d", len(captured.messages))
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
		// The store does not contain a cluster record, and the guest did not receive
		// a master response. The message can report only this guest's inability to
		// observe cluster health.
		"\n\nThis guest cannot see the ledger cluster\n" +
			"This guest cannot reach the ledger cluster (logins and audit trail), " +
			"so it cannot say whether the cluster is healthy.\n",
		"\n1. Confirm every ledger guest is up.\n",
	} {
		if !strings.Contains(body, sentence) {
			t.Errorf("body is missing %q:\n%s", sentence, body)
		}
	}
	if strings.Contains(body, "could not be") {
		t.Errorf("an empty store was read fine, so nothing may be described as unreadable:\n%s", body)
	}
	assertBackupAlarmPlainWords(t, captured.messages[0].Subject, body, cfg.BackupS3Endpoint)
}
