package ops

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestBackupStalenessAlarmMailThroughTheCommand runs the command with stale
// datable mechanisms in an object store. It checks one captured message: the
// subject identifies the guest and counts the faults, and the body presents
// each fault's phrase, its dated sentence in UTC when available, and its steps.
// The body excludes report lines and ends on the last step. The mailer appends
// its own footer after this composition.
func TestBackupStalenessAlarmMailThroughTheCommand(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	objects := ybExportRunObjects(
		t,
		"20260827T200000Z",
		newYBSnapshotManifest(
			"20260827T200000Z",
			"snap-1",
			"tack",
			[]string{"yb1"},
			ybTestArtifactNames(),
		),
	)
	objects[backupStatusKey(backupStalenessRehearsalName)] = marshalBackupStatusMarker(
		t,
		now.Add(-9*24*time.Hour),
		"restore drill passed every leg",
	)
	objects[backupStatusKey(backupStalenessReplicationName)] = marshalBackupStatusMarker(
		t,
		now.Add(-45*time.Minute),
		"0 dead nodes, 0 under-replicated tablets",
	)
	cfg := storedBackupStalenessConfig(t, newBackupTestStore(t, objects))

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
		// This guest did not receive a master response. Another guest's marker
		// cannot date its observation; the mail states only that this guest cannot
		// observe the cluster.
		"\n\nThis guest cannot see the ledger cluster\n" +
			"This guest cannot reach the ledger cluster (logins and audit trail), " +
			"so it cannot say whether the cluster is healthy.\n",
		"\n1. Confirm every ledger guest is up.\n",
	} {
		if !strings.Contains(message.Body, sentence) {
			t.Errorf("body is missing %q:\n%s", sentence, message.Body)
		}
	}

	if !strings.HasPrefix(message.Body, "Nightly ledger export is 40 hours old\n") ||
		!strings.HasSuffix(
			message.Body,
			"the alarm clears itself once this guest sees the cluster again.",
		) {
		t.Errorf(
			"the body must start on the first fault and end on the last step:\n%q",
			message.Body,
		)
	}

	for line := range strings.SplitSeq(strings.TrimSpace(report), "\n") {
		if strings.Contains(message.Body, line) {
			t.Errorf("body carries the report line %q:\n%s", line, message.Body)
		}
	}

	assertBackupAlarmPlainWords(t, message.Subject, message.Body, cfg.BackupS3Endpoint)
}

// TestBackupStalenessAlarmFDBWords checks FoundationDB alarm composition
// without running the command's container-dependent probe. It constructs a
// metric for a restorable point that stopped advancing and an unreadable status
// error that includes a blobstore URL with test credentials. It also checks the
// wording for a status that reports the absence of a restorable point.
func TestBackupStalenessAlarmFDBWords(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	threshold := 2 * time.Hour

	stopped := knownBackupStalenessMetric(
		ctx,
		backupStalenessFDBName,
		now,
		now.Add(-(15*time.Hour + 38*time.Minute)),
		threshold,
		"restorable through 2026-08-28T20:22:00Z",
	)
	subject := backupStalenessAlarmSubject(qaHost, []backupStalenessMetric{stopped})
	if subject != "[tack-qa] Product database backup stopped 15 hours 38 minutes ago" {
		t.Errorf("subject = %q", subject)
	}

	body := backupStalenessAlarmBody(cfg, []backupStalenessMetric{stopped})
	wantBody := "The product database backup (FoundationDB, continuous) last advanced at 8:22 PM UTC on Aug 28, 2026, " +
		"15 hours 38 minutes ago; the limit is 2 hours.\n" +
		"\n" +
		"1. On each data guest, run docker ps and confirm tack-fdb-backup-agent-1 is there.\n" +
		"2. On a data guest that is missing it, run docker logs tack-fdb-backup-agent-1 and read what it reports.\n" +
		"3. Confirm the object store accepts writes, then run docker compose restart fdb-backup-agent on that guest."
	if body != wantBody {
		t.Errorf("body mismatch:\n got=%q\nwant=%q", body, wantBody)
	}

	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)

	// An unreadable status cannot establish whether a restorable point exists.
	// The mail must not claim its absence or include the client's error text.
	unreadable := unknownBackupStalenessMetric(
		backupStalenessFDBName,
		threshold,
		backupStalenessUnreadable,
		"fdbbackup status failed: blobstore://test-access:test-secret@127.0.0.1:1/run?bucket=tack-backups",
	) // gitleaks:allow test placeholder
	subject = backupStalenessAlarmSubject(qaHost, []backupStalenessMetric{unreadable})
	if subject != "[tack-qa] Product database backup status could not be read" {
		t.Errorf("subject = %q", subject)
	}

	body = backupStalenessAlarmBody(cfg, []backupStalenessMetric{unreadable})
	if !strings.HasPrefix(body, "The product database backup's status could not be read.\n\n1. ") {
		t.Errorf("body must say the status could not be read, then the steps:\n%s", body)
	}

	if strings.Contains(body, "fdbbackup") || strings.Contains(body, "blobstore") {
		t.Errorf("an unreadable status must not carry the error text:\n%s", body)
	}

	if strings.Contains(body, "no restorable point") {
		t.Errorf("an unreadable status must not claim there is no restorable point:\n%s", body)
	}

	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)

	// A readable status that reports the absence of a restorable point supports
	// the corresponding mail statement. This case supplies that metric directly.
	none := unknownBackupStalenessMetric(
		backupStalenessFDBName,
		threshold,
		backupStalenessNeverRecorded,
		errFDBNoRestorablePoint.Error(),
	)
	subject = backupStalenessAlarmSubject(qaHost, []backupStalenessMetric{none})
	if subject != "[tack-qa] Product database backup has no restorable point" {
		t.Errorf("subject = %q", subject)
	}

	body = backupStalenessAlarmBody(cfg, []backupStalenessMetric{none})
	if !strings.HasPrefix(
		body,
		"The product database backup (FoundationDB, continuous) has no restorable point. "+
			"The last check reported: fdbbackup status reports no restorable backup.\n\n"+
			"1. On each data guest, run docker ps",
	) {
		t.Errorf("body does not say nothing is restorable, followed by the steps:\n%s", body)
	}

	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)
}
