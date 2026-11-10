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

// capturedBackupAlarm records the messages submitted to the alarm send
// function. The test replaces that function, while the command still reads a
// real object store and composes the production message.
type capturedBackupAlarm struct {
	messages []mailer.Message
	sendErr  error
}

// captureBackupAlarmSends replaces the alarm send function for the test and
// returns its captured messages and configured error.
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

// fixBackupStalenessClock sets the ops clock to a known instant for metric
// age calculations.
func fixBackupStalenessClock(t *testing.T, now time.Time) {
	t.Helper()
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = time.Now })
}

// unreachableBackupStalenessConfig configures refusing object store and
// ledger master connections. Each enabled mechanism lacks a measurable age and
// reports stale. The fresh backup directory represents a guest without prior
// alarm records.
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

// storedBackupStalenessConfig uses store's bucket and ledger masters that
// refuse connections. It sets the configured default thresholds and a test
// email recipient.
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

// runStaleBackupStalenessCheck runs the command once and requires the
// staleness error, regardless of whether the run sends mail. That error causes
// the command to exit unsuccessfully.
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

// alarmedBackupMetrics reads alarm records from the backup root. found is
// false when the state file does not exist.
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

// TestBackupStalenessAlarmMailsOncePerFault starts without a state file, as a
// newly provisioned guest would. A persistent fault must send one message on
// its first run and must not send duplicates on later runs. Every run must
// still return the staleness error.
func TestBackupStalenessAlarmMailsOncePerFault(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	if _, found := alarmedBackupMetrics(t, cfg); found {
		t.Fatal("a fresh backup root must hold no alarm state")
	}

	for run := 1; run <= 3; run++ {
		runStaleBackupStalenessCheck(t, cfg)
		if len(captured.messages) != 1 {
			t.Fatalf(
				"after run %d the fault must have mailed exactly once, sent %d",
				run,
				len(captured.messages),
			)
		}
	}

	alarmed, found := alarmedBackupMetrics(t, cfg)
	if !found {
		t.Fatal("an accepted mail must be remembered in the state file")
	}

	for _, name := range []string{
		backupStalenessExportName,
		backupStalenessRehearsalName,
		backupStalenessReplicationName,
	} {
		if _, ok := alarmed[name]; !ok {
			t.Errorf("state file does not remember %s: %v", name, alarmed)
		}
	}

	if len(alarmed) != 3 {
		t.Errorf(
			"state file remembers %d metrics, want the 3 that were read: %v",
			len(alarmed),
			alarmed,
		)
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
}

// TestBackupStalenessAlarmMailsAgainAfterAClear checks that recovery removes
// the alarm record without mailing. A later fault for that mechanism must send
// another message.
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

	// The first run observes a rehearsal without a recorded pass.
	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("the first fault must mail once, sent %d", len(captured.messages))
	}

	wantSubject := "[" + backupAlarmHost() + "] Restore rehearsal has never passed"
	if captured.messages[0].Subject != wantSubject {
		t.Errorf("subject = %q, want %q", captured.messages[0].Subject, wantSubject)
	}

	// The second run observes the drill pass and clears the fault without mail.
	store.put(rehearsalKey, freshRehearsal)
	var out bytes.Buffer
	if err := RunBackupStalenessCheck(context.Background(), cfg, &out); err != nil {
		t.Fatalf("every mechanism is fresh, so the check must pass: %v\n%s", err, out.String())
	}

	if len(captured.messages) != 1 {
		t.Fatalf("a clear must mail nothing, sent %d in total", len(captured.messages))
	}

	alarmed, _ := alarmedBackupMetrics(t, cfg)
	if _, ok := alarmed[backupStalenessRehearsalName]; ok {
		t.Fatalf("a cleared mechanism must be forgotten, state = %v", alarmed)
	}

	// The third run observes the missing marker as a new fault.
	store.remove(rehearsalKey)
	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 2 {
		t.Fatalf("a second fault must mail again, sent %d in total", len(captured.messages))
	}
}

// TestBackupStalenessAlarmRetriesAnUnsentMail checks a refused message. The
// run must return the staleness error rather than the transport error, must not
// record an accepted mail, and must retry on the next run. After acceptance,
// the command records the mail and suppresses later duplicates.
func TestBackupStalenessAlarmRetriesAnUnsentMail(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	sendErr := errors.New("smtp dial mail.example.test:587: connection refused")
	captured := captureBackupAlarmSends(t, sendErr)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")

	var out bytes.Buffer
	err := RunBackupStalenessCheck(context.Background(), cfg, &out)
	if err == nil {
		t.Fatal("a failed alarm mail must not turn a stale run into a success")
	}

	if errors.Is(err, sendErr) || strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("the mail failure must not become the returned error: %v", err)
	}

	if !strings.Contains(err.Error(), "past threshold") {
		t.Fatalf("the returned error must still be the staleness verdict: %v", err)
	}

	if len(captured.messages) != 1 {
		t.Fatalf("the alarm must have been attempted once, attempts = %d", len(captured.messages))
	}

	if alarmed, found := alarmedBackupMetrics(t, cfg); found && len(alarmed) != 0 {
		t.Fatalf("a refused mail must not be remembered, state = %v", alarmed)
	}

	captured.sendErr = nil
	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 2 {
		t.Fatalf("the next run must retry the unsent mail, attempts = %d", len(captured.messages))
	}

	if alarmed, found := alarmedBackupMetrics(t, cfg); !found || len(alarmed) != 3 {
		t.Fatalf("an accepted mail must be remembered, found = %v state = %v", found, alarmed)
	}

	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 2 {
		t.Fatalf("a remembered fault must not mail again, attempts = %d", len(captured.messages))
	}
}

// TestBackupStalenessAlarmWithNoRecipientAttemptsNoSend checks an unset mail
// recipient. The command must print its report, return the staleness error,
// skip the send function, and avoid recording acceptance. A later run with a
// recipient can still mail the fault.
func TestBackupStalenessAlarmWithNoRecipientAttemptsNoSend(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := unreachableBackupStalenessConfig(t, "")

	report := runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 0 {
		t.Fatalf("no recipient must mean no send attempt, got %d", len(captured.messages))
	}

	if report == "" {
		t.Fatal("the report must still be printed when there is nobody to mail")
	}

	if _, found := alarmedBackupMetrics(t, cfg); found {
		t.Fatal("nothing was mailed, so nothing may be remembered")
	}
}

// TestBackupStalenessCheckWithEverythingFreshMailsNothing runs the command
// with fresh export and success markers. A healthy run must not send an alarm;
// false alarms would undermine the alarm's usefulness.
func TestBackupStalenessCheckWithEverythingFreshMailsNothing(t *testing.T) {
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
			"every mechanism is inside its threshold, so the check must pass: %v\n%s",
			err,
			out.String(),
		)
	}

	if strings.Contains(out.String(), "STALE") {
		t.Fatalf("no mechanism should read STALE:\n%s", out.String())
	}

	if len(captured.messages) != 0 {
		t.Fatalf("a healthy run must mail nothing, sent %d", len(captured.messages))
	}
}

// marshalBackupStatusMarker encodes a success marker as the JSON body stored
// in the object store.
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
