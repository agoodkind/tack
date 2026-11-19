package ops

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

// backupOutageStart records the guest's last successful pre-outage observation.
var backupOutageStart = time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

// readBackupStoreBeforeOutage starts with export age 35h50m against 36h,
// rehearsal age one day, and a locally observed healthy cluster against 30m.
// Later master responses are follower pages without a usable leader report.
func readBackupStoreBeforeOutage(t *testing.T) (*backupTestStore, *config.Config) {
	t.Helper()
	fixBackupStalenessClock(t, backupOutageStart)
	objects := ybExportRunObjects(
		t,
		"20260828T001000Z",
		newYBSnapshotManifest(
			"20260828T001000Z",
			"snap-1",
			"tack",
			[]string{"yb1"},
			ybTestArtifactNames(),
		),
	)
	objects[backupStatusKey(backupStalenessRehearsalName)] = marshalBackupStatusMarker(
		t,
		backupOutageStart.Add(-24*time.Hour),
		"restore drill passed every leg",
	)
	store := newBackupTestStore(t, objects)
	cfg := storedBackupStalenessConfig(t, store)
	startYBMasterHealthServer(t, ybHealthAllGood, ybFollowerHealthPage)
	cfg.BackupYBMasterAddresses = "[::1]:7100"
	runFreshBackupStalenessCheck(t, cfg)

	return store, cfg
}

// runFreshBackupStalenessCheck requires command success and returns its report.
func runFreshBackupStalenessCheck(t *testing.T, cfg *config.Config) string {
	t.Helper()
	var out bytes.Buffer
	if err := RunBackupStalenessCheck(context.Background(), cfg, &out); err != nil {
		t.Fatalf("the run must report fresh metrics: %v\n%s", err, out.String())
	}

	return out.String()
}

// TestBackupStalenessAlarmSkipsEmailDuringShortStoreOutage checks historical
// dating before thresholds expire while still reporting unreadable observations.
func TestBackupStalenessAlarmSkipsEmailDuringShortStoreOutage(t *testing.T) {
	captured := captureBackupAlarmSends(t, nil)
	store, cfg := readBackupStoreBeforeOutage(t)

	store.stop()
	defer store.ensureStarted()
	fixBackupStalenessClock(t, backupOutageStart.Add(3*time.Minute))
	report := runFreshBackupStalenessCheck(t, cfg)

	if len(captured.messages) != 0 {
		t.Fatalf(
			"an outage inside every threshold must not send email, sent %q",
			captured.messages[0].Subject,
		)
	}

	if strings.Count(report, "unreadable, dated from the reading at 2026-08-29T12:00:00Z") != 2 {
		t.Errorf("the report must date unreadable readings from the prior observation:\n%s", report)
	}

	// Store availability does not determine this guest's cluster observation age.
	if !strings.Contains(report, "age=180s threshold=1800s FRESH") {
		t.Errorf("cluster age must use the guest's own observation:\n%s", report)
	}
}

// TestBackupStalenessAlarmMailsTheObjectStoreOnceAfterTheThreshold checks one
// email after historical ages exceed limits and suppresses repeated email.
func TestBackupStalenessAlarmMailsTheObjectStoreOnceAfterTheThreshold(t *testing.T) {
	captured := captureBackupAlarmSends(t, nil)
	store, cfg := readBackupStoreBeforeOutage(t)
	store.stop()
	defer store.ensureStarted()
	fixBackupStalenessClock(t, backupOutageStart.Add(3*time.Minute))
	runFreshBackupStalenessCheck(t, cfg)

	fixBackupStalenessClock(t, backupOutageStart.Add(31*time.Minute))
	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"two metrics above threshold must produce one email, sent %d",
			len(captured.messages),
		)
	}

	message := captured.messages[0]
	if message.Subject != "["+backupAlarmHost()+"] Backup check reported a problem" {
		t.Errorf("subject = %q", message.Subject)
	}

	for _, detail := range []string{
		"ledger-export age=130860s threshold=129600s STALE",
		"replication   age=1860s threshold=1800s STALE",
		"unreadable, dated from the reading at 2026-08-29T12:00:00Z",
		store.bucket.Endpoint,
	} {
		if !strings.Contains(message.Body, detail) {
			t.Errorf("the email does not include check output %q:\n%s", detail, message.Body)
		}
	}

	fixBackupStalenessClock(t, backupOutageStart.Add(40*time.Minute))
	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("a continuing fault must not repeat email, sent %d total", len(captured.messages))
	}
}

// TestBackupAlarmRetainsFaultUntilFreshStoreReading checks that historical
// freshness cannot clear another checker's recorded fault. A fresh live store
// reading must clear it without another email.
func TestBackupAlarmRetainsFaultUntilFreshStoreReading(t *testing.T) {
	captured := captureBackupAlarmSends(t, nil)
	store, cfg := readBackupStoreBeforeOutage(t)
	claimedAt := backupOutageStart.Add(time.Minute)
	saveBackupAlarmState(
		context.Background(),
		cfg,
		backupAlarmState{
			Alarmed:    map[string]time.Time{backupStalenessRehearsalName: claimedAt},
			Generation: 1,
		},
	)

	store.stop()
	defer store.ensureStarted()
	fixBackupStalenessClock(t, backupOutageStart.Add(3*time.Minute))
	runFreshBackupStalenessCheck(t, cfg)
	alarmed, _ := alarmedBackupMetrics(t, cfg)
	if at, held := alarmed[backupStalenessRehearsalName]; !held || !at.Equal(claimedAt) {
		t.Fatalf("an unreadable rehearsal must retain its recorded fault, state %v", alarmed)
	}

	cfg.BackupS3Endpoint = store.start()
	fixBackupStalenessClock(t, backupOutageStart.Add(4*time.Minute))
	runFreshBackupStalenessCheck(t, cfg)
	alarmed, _ = alarmedBackupMetrics(t, cfg)
	if _, held := alarmed[backupStalenessRehearsalName]; held {
		t.Fatalf("a fresh store reading must clear the rehearsal fault, state %v", alarmed)
	}

	if len(captured.messages) != 0 {
		t.Fatalf(
			"fault retention and recovery must not send email, sent %d",
			len(captured.messages),
		)
	}
}
