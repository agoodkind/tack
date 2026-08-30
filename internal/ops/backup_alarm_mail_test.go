package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"goodkind.io/send-email/mailer"

	"goodkind.io/tack/internal/config"
)

// capturedBackupAlarm records what the staleness alarm handed the mailer. Only
// the SMTP dial is substituted: the command still runs end to end, still talks
// to a real object-store client, and still composes the real message.
type capturedBackupAlarm struct {
	messages []mailer.Message
	sendErr  error
}

// captureBackupAlarmSends swaps the alarm's send seam for the duration of the
// test and returns the record of what it was asked to send.
func captureBackupAlarmSends(t *testing.T, sendErr error) *capturedBackupAlarm {
	t.Helper()
	captured := &capturedBackupAlarm{sendErr: sendErr}
	previous := backupAlarmSendFunc
	backupAlarmSendFunc = func(_ context.Context, _ *config.Config, message mailer.Message) error {
		captured.messages = append(captured.messages, message)
		return captured.sendErr
	}
	t.Cleanup(func() { backupAlarmSendFunc = previous })
	return captured
}

// fixBackupStalenessClock pins the ops clock so every metric dates against a
// known instant.
func fixBackupStalenessClock(t *testing.T, now time.Time) {
	t.Helper()
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = time.Now })
}

// unreachableBackupStalenessConfig is a host whose object store and ledger
// masters both refuse connections, so every mechanism is unmeasurable and
// therefore stale.
func unreachableBackupStalenessConfig(recipient string) *config.Config {
	return &config.Config{
		BackupS3Endpoint:                     "http://127.0.0.1:1",
		BackupS3AccessKey:                    "test-access", // gitleaks:allow test placeholder
		BackupS3SecretKey:                    "test-secret", // gitleaks:allow test placeholder
		BackupS3Region:                       "us-east-1",
		BackupS3BucketMain:                   "tack-backups",
		BackupYBMasterAddresses:              "127.0.0.1:7100",
		BackupFDBContinuous:                  false,
		BackupStalenessExportMaxSeconds:      129600,
		BackupStalenessRehearsalMaxSeconds:   691200,
		BackupStalenessReplicationMaxSeconds: 1800,
		BackupStalenessFDBMaxSeconds:         7200,
		BackupAlarmEmail:                     recipient,
	}
}

// TestBackupStalenessAlarmComposition pins the text of the alarm. The subject
// has to name the guest, because two guests run this check and an operator
// reading a phone notification must know which one is complaining. The body has
// to carry every metric line and name what is stale, because the mail is the
// only copy of the report anyone reads.
func TestBackupStalenessAlarmComposition(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	report := backupStalenessReport([]backupStalenessMetric{
		knownBackupStalenessMetric(context.Background(), backupStalenessExportName, now,
			now.Add(-2*time.Hour), 36*time.Hour, "newest complete run 20260829T100000Z"),
		unknownBackupStalenessMetric(backupStalenessRehearsalName, 8*24*time.Hour,
			"no backup-status/rehearsal.json in tack-backups"),
		unknownBackupStalenessMetric(backupStalenessReplicationName, 30*time.Minute,
			"2 dead nodes, 7 under-replicated tablets"),
	})

	tests := []struct {
		name  string
		host  string
		stale []string
	}{
		{
			name:  "one stale mechanism",
			host:  "tack-qa",
			stale: []string{backupStalenessRehearsalName},
		},
		{
			name:  "two stale mechanisms",
			host:  "tack-prod",
			stale: []string{backupStalenessRehearsalName, backupStalenessReplicationName},
		},
		{
			name:  "host that cannot name itself",
			host:  backupAlarmUnknownHost,
			stale: []string{backupStalenessReplicationName},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			subject := backupStalenessAlarmSubject(test.host)
			if !strings.Contains(subject, test.host) {
				t.Errorf("subject %q does not name the host %q", subject, test.host)
			}
			if !strings.Contains(subject, "stale") {
				t.Errorf("subject %q does not say the backups are stale", subject)
			}

			body := backupStalenessAlarmBody(test.host, report, test.stale)
			for _, line := range strings.Split(strings.TrimSuffix(report, "\n"), "\n") {
				if !strings.Contains(body, line) {
					t.Errorf("body is missing the metric line %q:\n%s", line, body)
				}
			}
			for _, name := range test.stale {
				if !strings.Contains(body, name) {
					t.Errorf("body does not name the stale mechanism %q:\n%s", name, body)
				}
			}
			if !strings.Contains(body, test.host) {
				t.Errorf("body does not name the host %q:\n%s", test.host, body)
			}
		})
	}
}

// TestBackupStalenessCheckMailsTheReportItPrinted runs the whole command on a
// host where nothing is measurable and proves the mail carries the same report
// the operator would have read on a terminal, addressed to the configured
// recipient. From stays empty on purpose: the library then sends as
// <hostname>-mailer@, the sender whose mail arrives, and pinning any address
// here would reintroduce the silently-dropped sender this change removed.
func TestBackupStalenessCheckMailsTheReportItPrinted(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := unreachableBackupStalenessConfig("backups@example.test")

	var out bytes.Buffer
	err := RunBackupStalenessCheck(context.Background(), cfg, &out)
	if err == nil {
		t.Fatal("unmeasurable backups must exit nonzero")
	}
	if len(captured.messages) != 1 {
		t.Fatalf("stale backups must send exactly one alarm, sent %d", len(captured.messages))
	}

	message := captured.messages[0]
	if message.To != "backups@example.test" {
		t.Errorf("To = %q, want the configured recipient", message.To)
	}
	if message.From != "" {
		t.Errorf("From = %q, want empty so the library uses the -mailer@ sender", message.From)
	}
	if message.Caller == "" {
		t.Error("Caller must identify the command that raised the alarm")
	}
	if !strings.Contains(message.Body, out.String()) {
		t.Errorf("the mail must carry the printed report verbatim:\nbody=%q\nreport=%q",
			message.Body, out.String())
	}
	for _, name := range []string{
		backupStalenessExportName,
		backupStalenessRehearsalName,
		backupStalenessReplicationName,
	} {
		if !strings.Contains(message.Body, name) {
			t.Errorf("the mail does not name the stale mechanism %q:\n%s", name, message.Body)
		}
	}
}

// TestBackupStalenessAlarmSendFailureDoesNotMaskTheStaleError is the rule the
// whole alarm rests on. Mail is best effort; the staleness verdict is not. A
// relay that refuses the message must not turn a stale-backup run into a
// mail-transport error, because the operator would then chase the mail path and
// never learn the backups had stopped.
func TestBackupStalenessAlarmSendFailureDoesNotMaskTheStaleError(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	sendErr := errors.New("smtp dial mail.example.test:587: connection refused")
	captured := captureBackupAlarmSends(t, sendErr)
	cfg := unreachableBackupStalenessConfig("backups@example.test")

	var out bytes.Buffer
	err := RunBackupStalenessCheck(context.Background(), cfg, &out)
	if err == nil {
		t.Fatal("a failed alarm mail must not turn a stale run into a success")
	}
	if len(captured.messages) != 1 {
		t.Fatalf("the alarm must still have been attempted, attempts = %d", len(captured.messages))
	}
	if errors.Is(err, sendErr) || strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("the mail failure must not become the returned error: %v", err)
	}
	if !strings.Contains(err.Error(), "past threshold") {
		t.Fatalf("the returned error must still be the staleness verdict: %v", err)
	}
	for _, name := range []string{
		backupStalenessExportName,
		backupStalenessRehearsalName,
		backupStalenessReplicationName,
	} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the returned error must still name the stale mechanism %q: %v", name, err)
		}
	}
}

// TestBackupStalenessAlarmWithNoRecipientAttemptsNoSend proves the command
// still works where no mail is configured, which is every local run: it reports
// and exits nonzero, and it does not try to dial anything.
func TestBackupStalenessAlarmWithNoRecipientAttemptsNoSend(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := unreachableBackupStalenessConfig("")

	var out bytes.Buffer
	err := RunBackupStalenessCheck(context.Background(), cfg, &out)
	if err == nil {
		t.Fatal("unmeasurable backups must exit nonzero with or without a recipient")
	}
	if !strings.Contains(err.Error(), "past threshold") {
		t.Fatalf("the returned error must be the staleness verdict: %v", err)
	}
	if len(captured.messages) != 0 {
		t.Fatalf("no recipient must mean no send attempt, got %d", len(captured.messages))
	}
	if out.Len() == 0 {
		t.Fatal("the report must still be printed when there is nobody to mail")
	}
}

// TestBackupStalenessCheckWithEverythingFreshMailsNothing runs the command
// against an object store holding a fresh export run and fresh markers. An
// alarm that also fired when the backups were healthy would be ignored within a
// week, so a fresh run must be silent.
func TestBackupStalenessCheckWithEverythingFreshMailsNothing(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)

	objects := fakeYBExportRunObjects(t, "20260829T100000Z",
		newYBSnapshotManifest("20260829T100000Z", "snap-1", "tack", []string{"yb1"}))
	maps.Copy(objects, map[string][]byte{
		backupStatusKey(backupStalenessRehearsalName): marshalBackupStatusMarker(t,
			now.Add(-6*time.Hour), "restore drill passed every leg"),
		backupStatusKey(backupStalenessReplicationName): marshalBackupStatusMarker(t,
			now.Add(-10*time.Minute), "0 dead nodes, 0 under-replicated tablets"),
	})
	// The command builds its own client from the config, so only the config
	// the fake store hands back is needed here.
	_, cfg := newFakeBackupObjectStore(t, "tack-backups", objects)
	cfg.BackupYBMasterAddresses = "127.0.0.1:7100"
	cfg.BackupFDBContinuous = false
	cfg.BackupStalenessExportMaxSeconds = 129600
	cfg.BackupStalenessRehearsalMaxSeconds = 691200
	cfg.BackupStalenessReplicationMaxSeconds = 1800
	cfg.BackupStalenessFDBMaxSeconds = 7200
	cfg.BackupAlarmEmail = "backups@example.test"

	var out bytes.Buffer
	if err := RunBackupStalenessCheck(context.Background(), cfg, &out); err != nil {
		t.Fatalf("every mechanism is inside its threshold, so the check must pass: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "STALE") {
		t.Fatalf("no mechanism should read STALE:\n%s", out.String())
	}
	if len(captured.messages) != 0 {
		t.Fatalf("a healthy run must mail nothing, sent %d", len(captured.messages))
	}
}

// marshalBackupStatusMarker builds the JSON body a success marker holds in the
// object store.
func marshalBackupStatusMarker(t *testing.T, at time.Time, detail string) []byte {
	t.Helper()
	body, err := json.Marshal(backupStatusMarker{At: at, Detail: detail})
	if err != nil {
		t.Fatalf("marshal marker: %v", err)
	}
	return body
}
