package ops

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestBackupStalenessAlarmMailThroughTheCommand runs the whole command against
// an object store whose every datable mechanism last succeeded too long ago,
// and reads the one mail as an operator would: the subject labels the
// environment and counts the faults, the body opens with the guest, says what
// happened with each time in UTC, and lists what to do under each fault's
// name. The printed report is not in it.
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
	if message.Subject != "[Tack QA] 3 backup problems need attention" {
		t.Errorf("subject = %q", message.Subject)
	}
	if message.Caller != backupAlarmCaller {
		t.Errorf("Caller = %q, want the command's name for the library's footer", message.Caller)
	}
	host := backupAlarmHost()
	for _, sentence := range []string{
		"QA, guest " + host + ". Not production.\n\nWHAT HAPPENED\n",
		"Nightly ledger export (daily ledger copy in the object store) last completed " +
			"8:00 PM UTC, Aug 27, 2026: 40 hours ago, limit 36 hours.\n",
		"Restore rehearsal (daily test restore) last passed 12:00 PM UTC, Aug 20, 2026: 9 days ago, limit 8 days.\n",
		"Ledger cluster (logins and audit trail) last healthy 11:15 AM UTC, Aug 29, 2026: 45 minutes ago, " +
			"limit 30 minutes. Last check: no master answered the health check: ",
		"\n\nWHAT TO DO\nNightly ledger export\n1. On the owner guest: journalctl -u tack-ledger-export.\n",
		"\nRestore rehearsal\n1. On the owner guest: journalctl -u tack-backup-restore-drill.\n",
		"\nLedger cluster\n1. Confirm every ledger guest is up.\n",
		"\n\nSent once per problem. Readings: tack-backup-staleness journal on " + host + ".",
	} {
		if !strings.Contains(message.Body, sentence) {
			t.Errorf("body is missing %q:\n%s", sentence, message.Body)
		}
	}
	if strings.Contains(message.Body, "Caller:") || strings.HasSuffix(message.Body, "\n") {
		t.Errorf("the body must leave the footer to the mailer and end on its last sentence:\n%q", message.Body)
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
	subject := backupStalenessAlarmSubject(qaScene, []backupStalenessMetric{stopped})
	if subject != "[Tack QA] Product database backup stopped 15 hours 38 minutes ago" {
		t.Errorf("subject = %q", subject)
	}
	body := backupStalenessAlarmBody(cfg, qaScene, []backupStalenessMetric{stopped})
	wantBody := "QA, guest tack-qa. Not production.\n" +
		"\n" +
		"WHAT HAPPENED\n" +
		"Product database backup (FoundationDB, continuous) last advanced 8:22 PM UTC, Aug 28, 2026: " +
		"15 hours 38 minutes ago, limit 2 hours.\n" +
		"\n" +
		"WHAT TO DO\n" +
		"1. On the owner guest, confirm the agent is running: docker ps (tack-fdb-backup-agent-1).\n" +
		"2. Read its log: docker logs tack-fdb-backup-agent-1.\n" +
		"3. Confirm the object store accepts writes, then: docker compose restart fdb-backup-agent.\n" +
		backupAlarmClosing
	if body != wantBody {
		t.Errorf("body mismatch:\n got=%q\nwant=%q", body, wantBody)
	}
	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)

	// A status that could not be read says nothing about what is restorable,
	// so the mail must not claim there is no restorable point.
	unreadable := unknownBackupStalenessMetric(backupStalenessFDBName, threshold, backupStalenessUnreadable,
		"fdbbackup status failed: blobstore://test-access:test-secret@127.0.0.1:1/run?bucket=tack-backups") // gitleaks:allow test placeholder
	subject = backupStalenessAlarmSubject(qaScene, []backupStalenessMetric{unreadable})
	if subject != "[Tack QA] Product database backup status could not be read" {
		t.Errorf("subject = %q", subject)
	}
	body = backupStalenessAlarmBody(cfg, qaScene, []backupStalenessMetric{unreadable})
	if !strings.Contains(body, "Product database backup (FoundationDB, continuous) status could not be read. "+
		"Last check: fdbbackup status failed: "+
		"blobstore://***REDACTED***:***REDACTED***@the object store/run?bucket=tack-backups.\n") {
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
	subject = backupStalenessAlarmSubject(qaScene, []backupStalenessMetric{none})
	if subject != "[Tack QA] Product database backup has no restorable point" {
		t.Errorf("subject = %q", subject)
	}
	body = backupStalenessAlarmBody(cfg, qaScene, []backupStalenessMetric{none})
	if !strings.Contains(body, "Product database backup (FoundationDB, continuous) has no restorable point. "+
		"Last check: fdbbackup status reports no restorable backup.\n"+
		"\nWHAT TO DO\n1. On the owner guest, confirm the agent is running") {
		t.Errorf("body does not say nothing is restorable, followed by what to do:\n%s", body)
	}
	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)
}
