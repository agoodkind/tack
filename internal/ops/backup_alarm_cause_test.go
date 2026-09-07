package ops

import (
	"strings"
	"testing"
	"time"
)

// TestBackupStalenessAlarmUnreadableWords runs the whole command against an
// object store and ledger masters that refuse every connection. Nothing here
// proves a backup was never made, only that nothing could be read, so every
// sentence must say the record could not be read and none may say the record
// does not exist. Each fault still gets its steps under its name.
func TestBackupStalenessAlarmUnreadableWords(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")

	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("three faults on one run must mail once, sent %d", len(captured.messages))
	}
	wantSubject := "[Tack QA] 3 backup problems need attention"
	if captured.messages[0].Subject != wantSubject {
		t.Errorf("subject = %q, want %q", captured.messages[0].Subject, wantSubject)
	}
	body := captured.messages[0].Body
	for _, sentence := range []string{
		"The nightly ledger export (the daily copy of the ledger database saved to the object store) " +
			"could not be dated, so whether a current copy exists is not known. " +
			"The last check saw: listing export runs failed: ",
		"The restore rehearsal (the daily test restore of both databases from the object store) " +
			"status could not be read, so whether it has passed recently is not known. " +
			"The last check saw: reading backup-status/rehearsal.json failed: ",
		"The ledger cluster (the database holding logins and the audit trail) health record could not " +
			"be read, so how long it has been unhealthy is not known. " +
			"The last check saw: reading backup-status/replication.json failed: ",
		"; this run observed: no master answered the health check: ",
		"\nWHAT TO DO\nNightly ledger export\n1. On the owner guest, read the export journal: ",
		"\nRestore rehearsal\n1. On the owner guest, read the drill journal: ",
		"\nLedger cluster\n1. Check every ledger guest is running and reachable.\n",
	} {
		if !strings.Contains(body, sentence) {
			t.Errorf("body is missing %q:\n%s", sentence, body)
		}
	}
	for _, claim := range []string{"has never", "no copy to restore", "never passed", "never been seen"} {
		if strings.Contains(body, claim) {
			t.Errorf("an unreadable store must not be described as empty (%q):\n%s", claim, body)
		}
	}
	assertBackupAlarmPlainWords(t, captured.messages[0].Subject, body, cfg.BackupS3Endpoint)
}

// TestBackupStalenessAlarmNeverRecordedWords runs the whole command against a
// reachable object store that holds nothing: no export run and no marker. That
// reading does prove no success was ever recorded, so the mail says so, in
// different words from the unreadable case, and each fault still gets its
// steps under its name.
func TestBackupStalenessAlarmNeverRecordedWords(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := storedBackupStalenessConfig(t, map[string][]byte{})

	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("three faults on one run must mail once, sent %d", len(captured.messages))
	}
	body := captured.messages[0].Body
	for _, sentence := range []string{
		"The nightly ledger export (the daily copy of the ledger database saved to the object store) " +
			"has never completed, so there is no copy to restore the ledger from. " +
			"The last check saw: no complete export run in tack-backups.\n",
		"The restore rehearsal (the daily test restore of both databases from the object store) " +
			"has never passed. The last check saw: no backup-status/rehearsal.json in tack-backups.\n",
		"The ledger cluster (the database holding logins and the audit trail) has never been seen healthy. " +
			"The last check saw: no backup-status/replication.json in tack-backups; " +
			"this run observed: no master answered the health check: ",
		"\nWHAT TO DO\nNightly ledger export\n1. On the owner guest, read the export journal: ",
		"\nRestore rehearsal\n1. On the owner guest, read the drill journal: ",
		"\nLedger cluster\n1. Check every ledger guest is running and reachable.\n",
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
