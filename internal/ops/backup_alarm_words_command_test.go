package ops

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestBackupStalenessAlarmMailThroughTheCommand runs the whole command against
// an object store whose every datable mechanism last succeeded too long ago,
// and reads the one mail as an operator would: the subject names the guest and
// counts the faults, and each block in the body opens with the fault's phrase,
// then its sentence with the time in UTC, then its steps. The printed report
// is not in it, and neither is a footer: the mailer appends its own.
func TestBackupStalenessAlarmMailThroughTheCommand(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	objects := fakeYBExportRunObjects(t, "20260827T200000Z",
		newYBSnapshotManifest("20260827T200000Z", "snap-1", "tack", []string{"yb1"}, ybTestArtifactNames()))
	objects[backupStatusKey(backupStalenessRehearsalName)] = marshalBackupStatusMarker(t,
		now.Add(-9*24*time.Hour), "restore drill passed every leg")
	objects[backupStatusKey(backupStalenessReplicationName)] = marshalBackupStatusMarker(t,
		now.Add(-45*time.Minute), "0 dead nodes, 0 under-replicated tablets")
	cfg := storedBackupStalenessConfig(t, objects)

	report := runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("three faults on one run must mail once, sent %d", len(captured.messages))
	}
	message := captured.messages[0]
	if message.Subject != "["+backupAlarmHost()+"] 3 backup problems" {
		t.Errorf("subject = %q", message.Subject)
	}
	if message.Caller != backupAlarmCaller {
		t.Errorf("Caller = %q, want the command's name for the library's footer", message.Caller)
	}
	for _, sentence := range []string{
		"Nightly ledger export is 40 hours old\n" +
			"The nightly ledger export (the daily copy of the ledger in the object store) last completed at " +
			"8:00 PM UTC on Aug 27, 2026, 40 hours ago; the limit is 36 hours.\n" +
			"1. On the owner guest, run journalctl -u tack-ledger-export.\n",
		"\n\nRestore rehearsal has not passed in 9 days\n" +
			"The restore rehearsal (the daily test restore) last passed at 12:00 PM UTC on Aug 20, 2026, " +
			"9 days ago; the limit is 8 days.\n" +
			"1. On the owner guest, run journalctl -u tack-backup-restore-drill.\n",
		"\n\nLedger cluster unhealthy for 45 minutes\n" +
			"The ledger cluster (logins and audit trail) was last healthy at 11:15 AM UTC on Aug 29, 2026, " +
			"45 minutes ago; the limit is 30 minutes. The last check reported: no master answered the health check: ",
		"\n1. Confirm every ledger guest is up.\n",
	} {
		if !strings.Contains(message.Body, sentence) {
			t.Errorf("body is missing %q:\n%s", sentence, message.Body)
		}
	}
	if !strings.HasPrefix(message.Body, "Nightly ledger export is 40 hours old\n") ||
		!strings.HasSuffix(message.Body, "the alarm clears itself once the cluster is healthy.") {
		t.Errorf("the body must start on the first fault and end on the last step:\n%q", message.Body)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(report), "\n") {
		if strings.Contains(message.Body, line) {
			t.Errorf("body carries the report line %q:\n%s", line, message.Body)
		}
	}
	assertBackupAlarmPlainWords(t, message.Subject, message.Body, cfg.BackupS3Endpoint)
}

// TestBackupStalenessAlarmFDBWords pins the FoundationDB fault's words. That
// leg needs a container runtime the command cannot be driven through here, so
// its mail is composed from the metric the probe would have produced: a known
// restorable point that stopped advancing, and a status that could not be read
// whose error echoes the blobstore URL with the credentials in it.
func TestBackupStalenessAlarmFDBWords(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	threshold := 2 * time.Hour

	stopped := knownBackupStalenessMetric(ctx, backupStalenessFDBName, now,
		now.Add(-(15*time.Hour + 38*time.Minute)), threshold, "restorable through 2026-08-28T20:22:00Z")
	subject := backupStalenessAlarmSubject(qaHost, []backupStalenessMetric{stopped})
	if subject != "[tack-qa] Product database backup stopped 15 hours 38 minutes ago" {
		t.Errorf("subject = %q", subject)
	}
	body := backupStalenessAlarmBody(cfg, []backupStalenessMetric{stopped})
	wantBody := "The product database backup (FoundationDB, continuous) last advanced at 8:22 PM UTC on Aug 28, 2026, " +
		"15 hours 38 minutes ago; the limit is 2 hours.\n" +
		"\n" +
		"1. On the owner guest, run docker ps and confirm tack-fdb-backup-agent-1 is running.\n" +
		"2. Run docker logs tack-fdb-backup-agent-1 and read what it reports.\n" +
		"3. Confirm the object store accepts writes, then run docker compose restart fdb-backup-agent."
	if body != wantBody {
		t.Errorf("body mismatch:\n got=%q\nwant=%q", body, wantBody)
	}
	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)

	// A status that could not be read says nothing about what is restorable,
	// so the mail must not claim there is no restorable point.
	unreadable := unknownBackupStalenessMetric(backupStalenessFDBName, threshold, backupStalenessUnreadable,
		"fdbbackup status failed: blobstore://test-access:test-secret@127.0.0.1:1/run?bucket=tack-backups") // gitleaks:allow test placeholder
	subject = backupStalenessAlarmSubject(qaHost, []backupStalenessMetric{unreadable})
	if subject != "[tack-qa] Product database backup status could not be read" {
		t.Errorf("subject = %q", subject)
	}
	body = backupStalenessAlarmBody(cfg, []backupStalenessMetric{unreadable})
	if !strings.HasPrefix(body, "The product database backup's status could not be read. "+
		"The last check reported: fdbbackup status failed: "+
		"blobstore://***REDACTED***:***REDACTED***@the object store/run?bucket=tack-backups.\n\n1. ") {
		t.Errorf("body does not carry the redacted reason:\n%s", body)
	}
	if strings.Contains(body, "no restorable point") {
		t.Errorf("an unreadable status must not claim there is no restorable point:\n%s", body)
	}
	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)

	// A status that was read and vouches for no restorable point is the one
	// case where nothing can be restored, and the mail may say so.
	none := unknownBackupStalenessMetric(backupStalenessFDBName, threshold, backupStalenessNeverRecorded,
		errFDBNoRestorablePoint.Error())
	subject = backupStalenessAlarmSubject(qaHost, []backupStalenessMetric{none})
	if subject != "[tack-qa] Product database backup has no restorable point" {
		t.Errorf("subject = %q", subject)
	}
	body = backupStalenessAlarmBody(cfg, []backupStalenessMetric{none})
	if !strings.HasPrefix(body, "The product database backup (FoundationDB, continuous) has no restorable point. "+
		"The last check reported: fdbbackup status reports no restorable backup.\n\n"+
		"1. On the owner guest, run docker ps") {
		t.Errorf("body does not say nothing is restorable, followed by the steps:\n%s", body)
	}
	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)
}
