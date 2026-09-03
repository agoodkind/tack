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
// does not exist. Each paragraph is still followed by what to check.
func TestBackupStalenessAlarmUnreadableWords(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")

	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("three faults on one run must mail once, sent %d", len(captured.messages))
	}
	body := captured.messages[0].Body
	for _, sentence := range []string{
		"The newest complete nightly ledger export could not be dated: listing export runs failed: ",
		"Whether a current export exists is not known until it can be read.\n" +
			"Check the tack-ledger-export timer on the owner guest",
		"The restore rehearsal's last pass could not be read: reading backup-status/rehearsal.json failed: ",
		"Whether the drill has passed recently is not known until the record can be read.\n" +
			"Check the tack-backup-restore-drill service journal on the owner guest.",
		"The ledger cluster's last healthy observation could not be read: " +
			"reading backup-status/replication.json failed: ",
		"; this run observed: no master answered the health check: ",
		"Check the ledger cluster's node and tablet state.",
	} {
		if !strings.Contains(body, sentence) {
			t.Errorf("body is missing %q:\n%s", sentence, body)
		}
	}
	for _, claim := range []string{"has no completed run", "has never", "no recorded pass", "holds no"} {
		if strings.Contains(body, claim) {
			t.Errorf("an unreadable store must not be described as empty (%q):\n%s", claim, body)
		}
	}
	assertBackupAlarmPlainWords(t, captured.messages[0].Subject, body, cfg.BackupS3Endpoint)
}

// TestBackupStalenessAlarmNeverRecordedWords runs the whole command against a
// reachable object store that holds nothing: no export run and no marker. That
// reading does prove no success was ever recorded, so the mail says so, in
// different words from the unreadable case, and each paragraph is still
// followed by what to check.
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
		"The object store holds no complete nightly ledger export, so there is none to restore " +
			"the ledger from: no complete export run in tack-backups.\n" +
			"Check the tack-ledger-export timer on the owner guest",
		"No restore rehearsal pass has been recorded in the object store: " +
			"no backup-status/rehearsal.json in tack-backups.\n" +
			"Check the tack-backup-restore-drill service journal on the owner guest.",
		"The ledger cluster has never been observed healthy: no backup-status/replication.json in " +
			"tack-backups; this run observed: no master answered the health check: ",
		"Check the ledger cluster's node and tablet state.",
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
