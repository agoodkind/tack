package ops

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestBackupStalenessAlarmMailThroughTheCommand checks production command
// composition, fault ordering, caller identity, recovery commands, and exclusion
// of machine-readable report lines and separately rendered footer metadata.
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
		t.Fatalf("three faults on one run must produce one email, sent %d", len(captured.messages))
	}

	message := captured.messages[0]
	if message.Subject != "["+backupAlarmHost()+"] 3 backup problems" {
		t.Errorf("subject = %q", message.Subject)
	}

	if message.Caller != backupAlarmCaller {
		t.Errorf("Caller = %q, want the command label for footer rendering", message.Caller)
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
		// Shared health markers cannot replace this guest's missing master observation.
		"\n\nThis guest cannot read ledger cluster health\n" +
			"This guest could not read a usable health report from the ledger cluster (logins and audit trail). " +
			"This check cannot establish whether the cluster is healthy.\n",
		"\n1. Confirm every ledger guest is up.\n",
	} {
		if !strings.Contains(message.Body, sentence) {
			t.Errorf("body is missing %q:\n%s", sentence, message.Body)
		}
	}

	if !strings.HasPrefix(message.Body, "Nightly ledger export is 40 hours old\n") ||
		!strings.HasSuffix(
			message.Body,
			"the checker clears the alarm after this guest reads a healthy response.",
		) {
		t.Errorf(
			"the body must start with the first fault and end with its final recovery step:\n%q",
			message.Body,
		)
	}

	for line := range strings.SplitSeq(strings.TrimSpace(report), "\n") {
		if strings.Contains(message.Body, line) {
			t.Errorf("body includes machine-readable report line %q:\n%s", line, message.Body)
		}
	}

	assertBackupAlarmPlainWords(t, message.Subject, message.Body, cfg.BackupS3Endpoint)
}

// TestBackupAlarmFormatsFDBFaultEvidence checks dated backup inactivity,
// unreadable status with credential-bearing errors, and confirmed absent
// restorable points without requiring a container probe in this composer test.
func TestBackupAlarmFormatsFDBFaultEvidence(t *testing.T) {
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
	// Client error text, including credentials, must not appear in email.
	unreadable := unknownBackupStalenessMetric(
		backupStalenessFDBName,
		threshold,
		backupStalenessUnreadable,
		"fdbbackup status failed: blobstore://test-access:test-secret@127.0.0.1:1/run?bucket=tack-backups",
	) // gitleaks:allow test placeholder
	subject = backupStalenessAlarmSubject(qaHost, []backupStalenessMetric{unreadable})
	if subject != "[tack-qa] This guest could not read product database backup status" {
		t.Errorf("subject = %q", subject)
	}

	body = backupStalenessAlarmBody(cfg, []backupStalenessMetric{unreadable})
	if !strings.HasPrefix(
		body,
		"This guest could not read the product database backup's status.\n\n1. ",
	) {
		t.Errorf("body must report unreadable status before recovery steps:\n%s", body)
	}

	if strings.Contains(body, "fdbbackup") || strings.Contains(body, "blobstore") {
		t.Errorf("unreadable-status email must not include client errors:\n%s", body)
	}

	if strings.Contains(body, "does not have a restorable point") {
		t.Errorf("unreadable status must not claim an absent restorable point:\n%s", body)
	}

	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)

	// Confirmed nonrestorable status permits an absent-point claim.
	none := unknownBackupStalenessMetric(
		backupStalenessFDBName,
		threshold,
		backupStalenessNeverRecorded,
		errFDBNoRestorablePoint.Error(),
	)
	subject = backupStalenessAlarmSubject(qaHost, []backupStalenessMetric{none})
	if subject != "[tack-qa] Product database backup does not have a restorable point" {
		t.Errorf("subject = %q", subject)
	}

	body = backupStalenessAlarmBody(cfg, []backupStalenessMetric{none})
	if !strings.HasPrefix(
		body,
		"The product database backup (FoundationDB, continuous) does not have a restorable point. "+
			"The last check reported: fdbbackup status does not report a restorable backup.\n\n"+
			"1. On each data guest, run docker ps",
	) {
		t.Errorf("body must report the confirmed missing point before recovery steps:\n%s", body)
	}

	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)
}
