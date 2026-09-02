package ops

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

// secondsValued matches a bare seconds figure such as "129600s", the unit the
// thresholds are configured in and the journal report prints. The mail is read
// in hours and minutes, so none may appear in it.
var secondsValued = regexp.MustCompile(`\b\d+s\b`)

// backupAlarmMachineWords matches everything the journal report says that the
// mail must not: the hyphenated metric names, standing alone rather than inside
// a systemd unit name such as tack-ledger-export, and the report's field labels.
var backupAlarmMachineWords = regexp.MustCompile(
	`(?:^|[^\w-])(` + backupStalenessExportName + `|` + backupStalenessFDBName + `|age=|threshold=|STALE)`)

// assertBackupAlarmPlainWords fails when the mail carries machine words, a
// seconds-valued figure, an object-store credential, or the store's endpoint.
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

// TestBackupStalenessAlarmMailIsPlainWords runs the whole command against an
// object store whose every datable mechanism last succeeded too long ago, and
// reads the one mail as an operator would: the subject counts the faults, and
// each paragraph says what stopped, when it last succeeded in UTC, how long
// ago in hours and minutes, and what to check. The printed report is not in it.
func TestBackupStalenessAlarmMailIsPlainWords(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	objects := fakeYBExportRunObjects(t, "20260827T200000Z",
		newYBSnapshotManifest("20260827T200000Z", "snap-1", "tack", []string{"yb1"}))
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
	wantSubject := "[tack] " + backupAlarmHost() + ": 3 backup mechanisms have stopped"
	if message.Subject != wantSubject {
		t.Errorf("subject = %q, want %q", message.Subject, wantSubject)
	}
	for _, sentence := range []string{
		"The newest complete nightly ledger export finished at 2026-08-27 20:00:00 UTC, 40h ago, " +
			"which is older than the 36h allowed for an export that runs daily.",
		"Check the tack-ledger-export timer on the owner guest and the tack-ledger-archive timer on each data guest.",
		"No restore rehearsal has passed within the 8 days allowed for a drill that runs daily: " +
			"the last pass was at 2026-08-20 12:00:00 UTC, 9 days ago.",
		"Check the tack-backup-restore-drill service journal on the owner guest.",
		"The ledger cluster has reported dead nodes or under-replicated tablets continuously since " +
			"2026-08-29 11:15:00 UTC, for 45m, which is longer than the 30m allowed. The cluster reports: ",
		"Check the ledger cluster's node and tablet state.",
		"This mail is sent once when the condition begins and does not repeat. " +
			"Every run's reading is in the tack-backup-staleness journal on " + backupAlarmHost() + ".",
	} {
		if !strings.Contains(message.Body, sentence) {
			t.Errorf("body is missing %q:\n%s", sentence, message.Body)
		}
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
	subject := backupStalenessAlarmSubject("tack-qa", []backupStalenessMetric{stopped})
	if subject != "[tack] tack-qa: FoundationDB backup stopped advancing 15h 38m ago" {
		t.Errorf("subject = %q", subject)
	}
	body := backupStalenessAlarmBody(cfg, "tack-qa", []backupStalenessMetric{stopped})
	wantBody := "The FoundationDB continuous backup's restorable point is 2026-08-28 20:22:00 UTC, " +
		"15h 38m ago, which is older than the 2h allowed. It normally trails the cluster by seconds, " +
		"so writes since that point are not restorable from the object store.\n" +
		"Check whether the container tack-fdb-backup-agent-1 is running and can reach the " +
		"object-store endpoint from the configuration.\n\n" +
		"This mail is sent once when the condition begins and does not repeat. " +
		"Every run's reading is in the tack-backup-staleness journal on tack-qa.\n"
	if body != wantBody {
		t.Errorf("body mismatch:\n got=%q\nwant=%q", body, wantBody)
	}
	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)

	unreadable := unknownBackupStalenessMetric(backupStalenessFDBName, threshold,
		"fdbbackup status failed: blobstore://test-access:test-secret@127.0.0.1:1/run?bucket=tack-backups") // gitleaks:allow test placeholder
	subject = backupStalenessAlarmSubject("tack-qa", []backupStalenessMetric{unreadable})
	if subject != "[tack] tack-qa: FoundationDB backup restorable point is unknown" {
		t.Errorf("subject = %q", subject)
	}
	body = backupStalenessAlarmBody(cfg, "tack-qa", []backupStalenessMetric{unreadable})
	if !strings.Contains(body, "The FoundationDB continuous backup's restorable point could not be read: "+
		"fdbbackup status failed: blobstore://***REDACTED***:***REDACTED***@the object store/run?bucket=tack-backups. "+
		"Writes since the last restorable point are not restorable from the object store.") {
		t.Errorf("body does not carry the redacted reason:\n%s", body)
	}
	assertBackupAlarmPlainWords(t, subject, body, cfg.BackupS3Endpoint)

	two := backupStalenessAlarmSubject("tack-qa", []backupStalenessMetric{stopped, unreadable})
	if two != "[tack] tack-qa: 2 backup mechanisms have stopped" {
		t.Errorf("two-fault subject = %q", two)
	}
}

// TestBackupAlarmClock pins the hours-and-minutes rendering the subject and
// body use.
func TestBackupAlarmClock(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{d: 15*time.Hour + 38*time.Minute, want: "15h 38m"},
		{d: 36 * time.Hour, want: "36h"},
		{d: 45 * time.Minute, want: "45m"},
		{d: 40 * time.Hour, want: "40h"},
		{d: 8 * 24 * time.Hour, want: "8 days"},
		{d: 9*24*time.Hour + 3*time.Hour + 20*time.Minute, want: "9 days 3h"},
		{d: 59 * time.Second, want: "0m"},
	}
	for _, test := range tests {
		if got := backupAlarmClock(test.d); got != test.want {
			t.Errorf("backupAlarmClock(%s) = %q, want %q", test.d, got, test.want)
		}
	}
}
