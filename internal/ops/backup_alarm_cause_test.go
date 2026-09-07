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
		"Nightly ledger export (daily ledger copy in the object store) could not be dated. " +
			"Last check: listing export runs failed: ",
		"Restore rehearsal (daily test restore) could not be read. " +
			"Last check: reading backup-status/rehearsal.json failed: ",
		"Ledger cluster (logins and audit trail) health could not be read. " +
			"Last check: reading backup-status/replication.json failed: ",
		"; this run observed: no master answered the health check: ",
		"\nWHAT TO DO\nNightly ledger export\n1. On the owner guest: journalctl -u tack-ledger-export.\n",
		"\nRestore rehearsal\n1. On the owner guest: journalctl -u tack-backup-restore-drill.\n",
		"\nLedger cluster\n1. Confirm every ledger guest is up.\n",
	} {
		if !strings.Contains(body, sentence) {
			t.Errorf("body is missing %q:\n%s", sentence, body)
		}
	}
	for _, claim := range []string{"has never", "never passed", "never been seen"} {
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
		"Nightly ledger export (daily ledger copy in the object store) has never completed. " +
			"Last check: no complete export run in tack-backups.\n",
		"Restore rehearsal (daily test restore) has never passed. " +
			"Last check: no backup-status/rehearsal.json in tack-backups.\n",
		"Ledger cluster (logins and audit trail) has never been seen healthy. " +
			"Last check: no backup-status/replication.json in tack-backups; " +
			"this run observed: no master answered the health check: ",
		"\nWHAT TO DO\nNightly ledger export\n1. On the owner guest: journalctl -u tack-ledger-export.\n",
		"\nRestore rehearsal\n1. On the owner guest: journalctl -u tack-backup-restore-drill.\n",
		"\nLedger cluster\n1. Confirm every ledger guest is up.\n",
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
