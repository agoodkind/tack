package ops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/testenv"
)

// TestBackupAlarmIncludesCheckOutput delivers the command's report through SMTP
// and checks that stored backup credentials do not appear in the email.
func TestBackupAlarmIncludesCheckOutput(t *testing.T) {
	mail := testenv.Mailpit(t)
	if err := mail.DeleteAll(t.Context()); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	const detail = "restore failed: blobstore://old-access:old-secret@backup.example.test/run"
	store := newBackupTestStore(
		t,
		map[string][]byte{
			backupStatusKey(backupStalenessRehearsalName): marshalBackupStatusMarker(
				t,
				now.Add(-9*24*time.Hour),
				detail,
			),
		},
	)
	cfg := storedBackupStalenessConfig(t, store)
	cfg.BackupAlarmMsmtprcPath = mail.Msmtprc
	report := runStaleBackupStalenessCheck(t, cfg)

	messages, err := mail.Messages(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(messages) != 1 {
		t.Fatalf("delivered %d messages, want one", len(messages))
	}

	message := messages[0]
	message.Text = strings.ReplaceAll(message.Text, "\r\n", "\n")
	if message.Subject != "["+backupAlarmHost()+"] Backup check reported a problem" {
		t.Errorf("subject = %q", message.Subject)
	}

	wantReport := strings.ReplaceAll(
		report,
		"old-access:old-secret",
		"***REDACTED***:***REDACTED***",
	)
	if !strings.HasPrefix(message.Text, wantReport) {
		t.Errorf("email does not contain the redacted check output:\n%s", message.Text)
	}

	for _, secret := range []string{
		cfg.BackupS3AccessKey,
		cfg.BackupS3SecretKey,
		"old-access",
		"old-secret",
	} {
		if strings.Contains(message.Text, secret) {
			t.Error("email exposes a backup credential")
		}
	}
}

// TestBackupAlarmReportsFDBCommandFailure runs fdbbackup against a closed port
// and verifies that its timeout preserves the previous reading and enters email.
func TestBackupAlarmReportsFDBCommandFailure(t *testing.T) {
	mail := testenv.Mailpit(t)
	if err := mail.DeleteAll(t.Context()); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	cfg.BackupAlarmMsmtprcPath = mail.Msmtprc
	cfg.BackupFDBContinuous = true
	cfg.BackupFDBImage = "foundationdb/foundationdb:7.4.6"
	cfg.BackupFDBNetwork = "tack-testenv"
	cfg.OpsFDBClusterDir = testenv.SharedDir(t)
	if err := os.WriteFile(
		filepath.Join(cfg.OpsFDBClusterDir, "fdb.cluster"),
		[]byte("timeout:timeout@127.0.0.1:1\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	last := backupLastReading{SucceededAt: now.Add(-3 * time.Hour), ReadAt: now.Add(-time.Minute)}
	saveBackupLastReadings(
		t.Context(),
		cfg,
		backupLastReadings{
			Readings: map[string]backupLastReading{backupStalenessFDBName: last},
		},
	)
	report := runStaleBackupStalenessCheck(t, cfg)

	for _, detail := range []string{
		"fdb-restorable age=10800s threshold=7200s STALE",
		"fdbbackup status failed (exit 1):",
		"transaction timed out",
	} {
		if !strings.Contains(report, detail) {
			t.Errorf("report does not include %q:\n%s", detail, report)
		}
	}

	if strings.Contains(report, errFDBNoRestorablePoint.Error()) {
		t.Errorf("timeout claims that a restorable backup does not exist:\n%s", report)
	}

	if got := loadBackupLastReadings(
		context.Background(),
		cfg,
	).Readings[backupStalenessFDBName]; got != last {
		t.Errorf("previous reading = %+v, want %+v", got, last)
	}

	messages, err := mail.Messages(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(messages) != 1 {
		t.Fatalf("delivered %d messages, want one", len(messages))
	}

	body := strings.ReplaceAll(messages[0].Text, "\r\n", "\n")
	if !strings.Contains(body, report) {
		t.Fatal("the email does not include the failed check output")
	}
}
