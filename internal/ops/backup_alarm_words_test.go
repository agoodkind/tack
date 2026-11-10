package ops

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

// secondsValued matches a bare seconds value such as "129600s". Threshold
// configuration and the journal report use seconds. Alarm messages must use
// hours and minutes instead.
var secondsValued = regexp.MustCompile(`\b\d+s\b`)

// backupAlarmMachineWords matches journal terms that alarm messages must
// exclude. It matches hyphenated metric names outside systemd unit names such
// as tack-ledger-export, and it matches the report's field labels.
var backupAlarmMachineWords = regexp.MustCompile(
	`(?:^|[^\w-])(` + backupStalenessExportName + `|` + backupStalenessFDBName + `|age=|threshold=|STALE)`,
)

// assertBackupAlarmPlainWords rejects machine terms, bare seconds values,
// object-store credentials, and the store endpoint in alarm messages.
func assertBackupAlarmPlainWords(t *testing.T, subject, body, endpoint string) {
	t.Helper()

	for _, text := range []string{subject, body} {
		if match := backupAlarmMachineWords.FindString(text); match != "" {
			t.Errorf("the mail carries the machine word %q:\n%s", strings.TrimSpace(match), text)
		}

		if match := secondsValued.FindString(text); match != "" {
			t.Errorf("the mail carries a seconds-valued figure %q:\n%s", match, text)
		}

		for _, secret := range []string{"test-access", "test-secret", endpoint} {
			if secret != "" && strings.Contains(text, secret) {
				t.Errorf("the mail leaks %q:\n%s", secret, text)
			}
		}
	}
}

// qaHost identifies the guest that supplies the test alarm messages.
const qaHost = "tack-qa"

// TestBackupStalenessAlarmOneFaultMail checks the complete message for a
// ledger cluster last healthy 32 minutes ago against a 30-minute limit.
// The subject identifies the guest and fault. The body contains the factual
// sentence with its UTC time and the recovery steps.
func TestBackupStalenessAlarmOneFaultMail(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 16, 23, 0, 0, time.UTC)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	fault := knownBackupStalenessMetric(
		ctx,
		backupStalenessReplicationName,
		now,
		now.Add(-32*time.Minute),
		30*time.Minute,
		"1 dead nodes, 4 under-replicated tablets",
	)
	faults := []backupStalenessMetric{fault}

	subject := backupStalenessAlarmSubject(qaHost, faults)
	if subject != "[tack-qa] Ledger cluster unhealthy for 32 minutes" {
		t.Errorf("subject = %q", subject)
	}

	body := backupStalenessAlarmBody(cfg, faults)
	wantBody := "The ledger cluster (logins and audit trail) was last healthy at 3:51 PM UTC on Sep 6, 2026, " +
		"32 minutes ago; the limit is 30 minutes. " +
		"The last check reported: 1 dead nodes, 4 under-replicated tablets.\n" +
		"\n" +
		"1. Confirm every ledger guest is up.\n" +
		"2. On the owner guest, confirm every node is alive on the ledger master page.\n" +
		"3. Wait for tablets to re-copy; the alarm clears itself once the cluster is healthy."
	if body != wantBody {
		t.Errorf("body mismatch:\n got=%q\nwant=%q", body, wantBody)
	}

	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)
}

// TestBackupStalenessAlarmThreeFaultMail checks the complete message for
// three simultaneous faults. The subject counts the faults. Each body block
// contains the fault's phrase, its factual sentence, and its recovery steps
// in that order.
func TestBackupStalenessAlarmThreeFaultMail(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	faults := []backupStalenessMetric{
		knownBackupStalenessMetric(
			ctx,
			backupStalenessExportName,
			now,
			now.Add(-40*time.Hour),
			36*time.Hour,
			"newest complete run 20260827T200000Z",
		),
		knownBackupStalenessMetric(
			ctx,
			backupStalenessRehearsalName,
			now,
			now.Add(-9*24*time.Hour),
			8*24*time.Hour,
			"restore drill passed every leg",
		),
		knownBackupStalenessMetric(
			ctx,
			backupStalenessReplicationName,
			now,
			now.Add(-45*time.Minute),
			30*time.Minute,
			"1 dead nodes, 2 under-replicated tablets",
		),
	}

	subject := backupStalenessAlarmSubject(qaHost, faults)
	if subject != "[tack-qa] 3 backup problems" {
		t.Errorf("subject = %q", subject)
	}

	body := backupStalenessAlarmBody(cfg, faults)
	wantBody := "Nightly ledger export is 40 hours old\n" +
		"The nightly ledger export (the daily copy of the ledger in the object store) last completed at " +
		"8:00 PM UTC on Aug 27, 2026, 40 hours ago; the limit is 36 hours.\n" +
		"1. On the owner guest, run journalctl -u tack-ledger-export.\n" +
		"2. On each data guest, run journalctl -u tack-ledger-archive.\n" +
		"3. Confirm the object store accepts writes, then run systemctl start tack-ledger-export.\n" +
		"\n" +
		"Restore rehearsal has not passed in 9 days\n" +
		"The restore rehearsal (the daily test restore) last passed at 12:00 PM UTC on Aug 20, 2026, " +
		"9 days ago; the limit is 8 days.\n" +
		"1. On the owner guest, run journalctl -u tack-backup-restore-drill.\n" +
		"2. Fix what it names, then run systemctl start tack-backup-restore-drill.\n" +
		"\n" +
		"Ledger cluster unhealthy for 45 minutes\n" +
		"The ledger cluster (logins and audit trail) was last healthy at 11:15 AM UTC on Aug 29, 2026, " +
		"45 minutes ago; the limit is 30 minutes. " +
		"The last check reported: 1 dead nodes, 2 under-replicated tablets.\n" +
		"1. Confirm every ledger guest is up.\n" +
		"2. On the owner guest, confirm every node is alive on the ledger master page.\n" +
		"3. Wait for tablets to re-copy; the alarm clears itself once the cluster is healthy."
	if body != wantBody {
		t.Errorf("body mismatch:\n got=%q\nwant=%q", body, wantBody)
	}

	if strings.Contains(body, "20260827T200000Z") {
		t.Errorf("the export run id must not reach the mail:\n%s", body)
	}

	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)
}

// TestBackupAlarmClock checks the duration wording that the subject and body
// use.
func TestBackupAlarmClock(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{d: 15*time.Hour + 38*time.Minute, want: "15 hours 38 minutes"},
		{d: 36 * time.Hour, want: "36 hours"},
		{d: 45 * time.Minute, want: "45 minutes"},
		{d: time.Hour + time.Minute, want: "1 hour 1 minute"},
		{d: 40 * time.Hour, want: "40 hours"},
		{d: 8 * 24 * time.Hour, want: "8 days"},
		{d: 9*24*time.Hour + 3*time.Hour + 20*time.Minute, want: "9 days 3 hours"},
		{d: 2*24*time.Hour + time.Hour, want: "2 days 1 hour"},
		{d: 59 * time.Second, want: "0 minutes"},
	}
	for _, test := range tests {
		if got := backupAlarmClock(test.d); got != test.want {
			t.Errorf("backupAlarmClock(%s) = %q, want %q", test.d, got, test.want)
		}
	}
}
