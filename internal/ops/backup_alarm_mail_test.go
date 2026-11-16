package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	"goodkind.io/send-email/mailer"

	"goodkind.io/tack/internal/config"
)

// capturedBackupAlarm records messages from the production command with only
// SMTP delivery substituted. Store access and message composition remain real.
type capturedBackupAlarm struct {
	messages []mailer.Message
	sendErr  error
}

// captureBackupAlarmSends records delivery attempts and restores the sender
// after the test.
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

// fixBackupStalenessClock fixes all metric comparison times for a test.
func fixBackupStalenessClock(t *testing.T, now time.Time) {
	t.Helper()
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = time.Now })
}

// unreachableBackupStalenessConfig supplies refused store and master
// connections with a fresh local state directory. Undatable metrics count as stale.
func unreachableBackupStalenessConfig(t *testing.T, recipient string) *config.Config {
	t.Helper()
	backupS3Attempts(t, 1)

	return &config.Config{
		BackupRoot:                           t.TempDir(),
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

// storedBackupStalenessConfig uses the fixture bucket, refused master
// connections, explicit thresholds, and a test email recipient.
func storedBackupStalenessConfig(t *testing.T, store *backupTestStore) *config.Config {
	t.Helper()
	cfg := store.config()
	cfg.BackupRoot = t.TempDir()
	cfg.BackupYBMasterAddresses = "127.0.0.1:7100"
	cfg.BackupFDBContinuous = false
	cfg.BackupStalenessExportMaxSeconds = 129600
	cfg.BackupStalenessRehearsalMaxSeconds = 691200
	cfg.BackupStalenessReplicationMaxSeconds = 1800
	cfg.BackupStalenessFDBMaxSeconds = 7200
	cfg.BackupAlarmEmail = "backups@example.test"

	return cfg
}

// runStaleBackupStalenessCheck verifies the command's stale error independently
// of email delivery and returns its report.
func runStaleBackupStalenessCheck(t *testing.T, cfg *config.Config) string {
	t.Helper()
	var out bytes.Buffer
	err := RunBackupStalenessCheck(context.Background(), cfg, &out)
	if err == nil {
		t.Fatal("a stale run must exit nonzero")
	}

	if !strings.Contains(err.Error(), "past threshold") {
		t.Fatalf("the returned error must be the staleness verdict: %v", err)
	}

	return out.String()
}

// alarmedBackupMetrics reads locally recorded faults or returns found=false
// when the state file does not exist.
func alarmedBackupMetrics(
	t *testing.T,
	cfg *config.Config,
) (alarmed map[string]time.Time, found bool) {
	t.Helper()
	body, err := os.ReadFile(backupAlarmStatePath(cfg))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}

	if err != nil {
		t.Fatalf("read alarm state: %v", err)
	}

	var state backupAlarmState
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode alarm state %q: %v", body, err)
	}

	return state.Alarmed, true
}

// TestBackupStalenessAlarmMailsOncePerFault checks one email across repeated
// stale runs, persisted acceptance state, and configured message identity.
func TestBackupStalenessAlarmMailsOncePerFault(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	if _, found := alarmedBackupMetrics(t, cfg); found {
		t.Fatal("a fresh backup root must not contain alarm state")
	}

	for run := 1; run <= 3; run++ {
		runStaleBackupStalenessCheck(t, cfg)
		if len(captured.messages) != 1 {
			t.Fatalf(
				"after run %d the fault must produce exactly one email, sent %d",
				run,
				len(captured.messages),
			)
		}
	}

	alarmed, found := alarmedBackupMetrics(t, cfg)
	if !found {
		t.Fatal("state must record accepted email")
	}

	for _, name := range []string{
		backupStalenessExportName,
		backupStalenessRehearsalName,
		backupStalenessReplicationName,
	} {
		if _, ok := alarmed[name]; !ok {
			t.Errorf("state does not record fault %s: %v", name, alarmed)
		}
	}

	if len(alarmed) != 3 {
		t.Errorf("recorded faults = %d, want all 3 observed metrics: %v", len(alarmed), alarmed)
	}

	message := captured.messages[0]
	if message.To != "backups@example.test" {
		t.Errorf("To = %q, want the configured recipient", message.To)
	}

	if message.From != "" {
		t.Errorf("From = %q, want empty for the library's -mailer@ sender", message.From)
	}

	if message.Caller == "" {
		t.Error("Caller must identify the command that raised the alarm")
	}
}

// TestBackupStalenessAlarmMailsAgainAfterAClear checks fault-state removal
// after recovery and a new email after a later failure.
func TestBackupStalenessAlarmMailsAgainAfterAClear(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	objects := ybExportRunObjects(
		t,
		"20260829T100000Z",
		newYBSnapshotManifest(
			"20260829T100000Z",
			"snap-1",
			"tack",
			[]string{"yb1"},
			ybTestArtifactNames(),
		),
	)
	objects[backupStatusKey(backupStalenessReplicationName)] = marshalBackupStatusMarker(
		t,
		now.Add(-10*time.Minute),
		"0 dead nodes, 0 under-replicated tablets",
	)
	store := newBackupTestStore(t, objects)
	cfg := storedBackupStalenessConfig(t, store)
	seesHealthyCluster(t, cfg)
	rehearsalKey := backupStatusKey(backupStalenessRehearsalName)
	freshRehearsal := marshalBackupStatusMarker(
		t,
		now.Add(-6*time.Hour),
		"restore drill passed every leg",
	)

	// The first run observes an absent rehearsal marker.
	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("the first fault must email once, sent %d", len(captured.messages))
	}

	wantSubject := "[" + backupAlarmHost() + "] Restore rehearsal has never passed"
	if captured.messages[0].Subject != wantSubject {
		t.Errorf("subject = %q, want %q", captured.messages[0].Subject, wantSubject)
	}

	// A fresh marker clears the fault without another email.
	store.put(rehearsalKey, freshRehearsal)
	var out bytes.Buffer
	if err := RunBackupStalenessCheck(context.Background(), cfg, &out); err != nil {
		t.Fatalf("the check must pass with every mechanism fresh: %v\n%s", err, out.String())
	}

	if len(captured.messages) != 1 {
		t.Fatalf("recovery must not send email, sent %d total", len(captured.messages))
	}

	alarmed, _ := alarmedBackupMetrics(t, cfg)
	if _, ok := alarmed[backupStalenessRehearsalName]; ok {
		t.Fatalf("recovery must remove the recorded fault, state = %v", alarmed)
	}

	// Removing the marker creates a distinct second fault.
	store.remove(rehearsalKey)
	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 2 {
		t.Fatalf("a second fault must email again, sent %d total", len(captured.messages))
	}
}

// TestBackupStalenessAlarmRetriesAnUnsentMail checks unchanged stale verdicts,
// absence of accepted state after delivery failure, and retry until acceptance.
func TestBackupStalenessAlarmRetriesAnUnsentMail(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	sendErr := errors.New("smtp dial mail.example.test:587: connection refused")
	captured := captureBackupAlarmSends(t, sendErr)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")

	var out bytes.Buffer
	err := RunBackupStalenessCheck(context.Background(), cfg, &out)
	if err == nil {
		t.Fatal("failed alarm delivery must not make a stale run succeed")
	}

	if errors.Is(err, sendErr) || strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("delivery failure must not replace the returned stale verdict: %v", err)
	}

	if !strings.Contains(err.Error(), "past threshold") {
		t.Fatalf("the returned error must remain the staleness verdict: %v", err)
	}

	if len(captured.messages) != 1 {
		t.Fatalf("delivery attempts = %d, want 1", len(captured.messages))
	}

	if alarmed, found := alarmedBackupMetrics(t, cfg); found && len(alarmed) != 0 {
		t.Fatalf("refused delivery must not record acceptance, state = %v", alarmed)
	}

	captured.sendErr = nil
	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 2 {
		t.Fatalf("the next run must retry delivery, attempts = %d", len(captured.messages))
	}

	if alarmed, found := alarmedBackupMetrics(t, cfg); !found || len(alarmed) != 3 {
		t.Fatalf("state must record acceptance, found = %v state = %v", found, alarmed)
	}

	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 2 {
		t.Fatalf(
			"recorded acceptance must suppress duplicate email, attempts = %d",
			len(captured.messages),
		)
	}
}

// TestBackupStalenessAlarmSkipsDeliveryWithoutRecipient checks reporting and
// stale failure without SMTP attempts or accepted-fault state.
func TestBackupStalenessAlarmSkipsDeliveryWithoutRecipient(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := unreachableBackupStalenessConfig(t, "")

	report := runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 0 {
		t.Fatalf(
			"an absent recipient must prevent delivery attempts, got %d",
			len(captured.messages),
		)
	}

	if report == "" {
		t.Fatal("the command must still print a report without a recipient")
	}

	if _, found := alarmedBackupMetrics(t, cfg); found {
		t.Fatal("skipped delivery must not create accepted-fault state")
	}
}

// TestBackupStalenessCheckSkipsEmailForFreshMetrics checks fresh report output
// without an alarm email against complete export and marker fixtures.
func TestBackupStalenessCheckSkipsEmailForFreshMetrics(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)

	objects := ybExportRunObjects(
		t,
		"20260829T100000Z",
		newYBSnapshotManifest(
			"20260829T100000Z",
			"snap-1",
			"tack",
			[]string{"yb1"},
			ybTestArtifactNames(),
		),
	)
	maps.Copy(
		objects,
		map[string][]byte{
			backupStatusKey(backupStalenessRehearsalName): marshalBackupStatusMarker(
				t,
				now.Add(-6*time.Hour),
				"restore drill passed every leg",
			),
			backupStatusKey(backupStalenessReplicationName): marshalBackupStatusMarker(
				t,
				now.Add(-10*time.Minute),
				"0 dead nodes, 0 under-replicated tablets",
			),
		},
	)
	cfg := storedBackupStalenessConfig(t, newBackupTestStore(t, objects))
	seesHealthyCluster(t, cfg)

	var out bytes.Buffer
	if err := RunBackupStalenessCheck(context.Background(), cfg, &out); err != nil {
		t.Fatalf(
			"the check must pass with every mechanism inside its threshold: %v\n%s",
			err,
			out.String(),
		)
	}

	if strings.Contains(out.String(), "STALE") {
		t.Fatalf("fresh metrics must not report STALE:\n%s", out.String())
	}

	if len(captured.messages) != 0 {
		t.Fatalf("a healthy run must not send email, sent %d", len(captured.messages))
	}
}

func marshalBackupStatusMarker(t *testing.T, at time.Time, detail string) []byte {
	t.Helper()
	body, err := json.Marshal(backupStatusMarker{
		At:     at,
		Detail: detail,
	})
	if err != nil {
		t.Fatalf("marshal marker: %v", err)
	}

	return body
}
