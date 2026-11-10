package ops

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

// backupOutageStart is when the guest last read the store before it stopped
// answering.
var backupOutageStart = time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

// readBackupStoreBeforeOutage runs a fresh check before the outage. The
// export age is 35 hours 50 minutes against a 36-hour limit, and the rehearsal
// passed one day earlier. The master's first response reports a healthy
// cluster, establishing this guest's observation against a 30-minute limit.
// Later responses contain a follower page without leader health, and the
// cluster age uses that first observation. The helper returns the store and
// configuration still using that store.
func readBackupStoreBeforeOutage(t *testing.T) (*backupTestStore, *config.Config) {
	t.Helper()
	fixBackupStalenessClock(t, backupOutageStart)
	objects := ybExportRunObjects(t, "20260828T001000Z",
		newYBSnapshotManifest("20260828T001000Z", "snap-1", "tack", []string{"yb1"}, ybTestArtifactNames()))
	objects[backupStatusKey(backupStalenessRehearsalName)] = marshalBackupStatusMarker(t,
		backupOutageStart.Add(-24*time.Hour), "restore drill passed every leg")
	store := newBackupTestStore(t, objects)
	cfg := storedBackupStalenessConfig(t, store)
	startYBMasterHealthServer(t, ybHealthAllGood, ybFollowerHealthPage)
	cfg.BackupYBMasterAddresses = "[::1]:7100"
	runFreshBackupStalenessCheck(t, cfg)
	return store, cfg
}

// runFreshBackupStalenessCheck runs the command, requires success without a
// staleness error, and returns the report.
func runFreshBackupStalenessCheck(t *testing.T, cfg *config.Config) string {
	t.Helper()
	var out bytes.Buffer
	if err := RunBackupStalenessCheck(context.Background(), cfg, &out); err != nil {
		t.Fatalf("the run must find nothing stale: %v\n%s", err, out.String())
	}
	return out.String()
}

// TestBackupStalenessAlarmStaysQuietThroughAShortStoreOutage reproduces the
// QA mail scenario recorded for 2026-09-19. The store stops minutes after a
// fresh check. Remembered observations still fall within every age limit, and
// the command must return success without mail while reporting unreadable
// current observations.
func TestBackupStalenessAlarmStaysQuietThroughAShortStoreOutage(t *testing.T) {
	captured := captureBackupAlarmSends(t, nil)
	store, cfg := readBackupStoreBeforeOutage(t)

	store.stop()
	defer store.ensureStarted()
	fixBackupStalenessClock(t, backupOutageStart.Add(3*time.Minute))
	report := runFreshBackupStalenessCheck(t, cfg)

	if len(captured.messages) != 0 {
		t.Fatalf("an outage inside every limit must not mail, sent %q", captured.messages[0].Subject)
	}
	if strings.Count(report, "unreadable, dated from the reading at 2026-08-29T12:00:00Z") != 2 {
		t.Errorf("the report must date each unreadable reading from the last run:\n%s", report)
	}
	// The cluster age uses this guest's first observation independently of the
	// object-store outage.
	if !strings.Contains(report, "age=180s threshold=1800s FRESH") {
		t.Errorf("the cluster reading must age from this guest's own observation:\n%s", report)
	}
}

// TestBackupStalenessAlarmMailsTheObjectStoreOnceAfterTheThreshold keeps the
// store down until two remembered ages exceed their limits. That run must send
// one message with the last store observation time in one store paragraph and
// plain text for each mechanism, without client error text. A later run during
// the outage must not send a duplicate.
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
		t.Fatalf("two mechanisms past their limits must mail once, sent %d", len(captured.messages))
	}
	message := captured.messages[0]
	if message.Subject != "["+backupAlarmHost()+"] 2 backup problems" {
		t.Errorf("subject = %q", message.Subject)
	}
	wantBody := "The object store (where the backups are kept) did not answer; " +
		"this guest last read it at 12:00 PM UTC on Aug 29, 2026. " +
		"Confirm the object store guest is running before the steps below.\n" +
		"\n" +
		"Nightly ledger export status could not be read\n" +
		"The nightly ledger export's newest copy could not be dated. The newest copy this guest last read " +
		"was made at 12:10 AM UTC on Aug 28, 2026, 36 hours 21 minutes ago; the limit is 36 hours.\n" +
		"1. On the owner guest, run journalctl -u tack-ledger-export.\n" +
		"2. On each data guest, run journalctl -u tack-ledger-archive.\n" +
		"3. Confirm the object store accepts writes, then run systemctl start tack-ledger-export.\n" +
		"\n" +
		"This guest cannot see the ledger cluster\n" +
		"This guest cannot reach the ledger cluster (logins and audit trail), so it cannot say whether " +
		"the cluster is healthy. It last saw the cluster healthy " +
		"at 12:00 PM UTC on Aug 29, 2026, 31 minutes ago; the limit is 30 minutes.\n" +
		"1. Confirm every ledger guest is up.\n" +
		"2. From this guest, confirm the ledger master page answers.\n" +
		"3. Fix what blocks it; the alarm clears itself once this guest sees the cluster again."
	if message.Body != wantBody {
		t.Errorf("body mismatch:\n got=%q\nwant=%q", message.Body, wantBody)
	}
	for _, raw := range []string{"operation error", "StatusCode", "http", store.bucket.Endpoint, "request send failed"} {
		if strings.Contains(message.Subject+message.Body, raw) {
			t.Errorf("the mail carries the client's error text %q:\n%s", raw, message.Body)
		}
	}
	assertBackupAlarmPlainWords(t, message.Subject, message.Body, cfg.BackupS3Endpoint)

	fixBackupStalenessClock(t, backupOutageStart.Add(40*time.Minute))
	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("a fault that lasts must not mail again, sent %d in total", len(captured.messages))
	}
}

// TestBackupStalenessAlarmHoldsAFaultThroughAnOutage starts with a rehearsal
// fault representing another checker's mail. The store becomes unreadable
// while the remembered rehearsal age remains fresh. That older observation
// must not clear the fault. A fresh rehearsal observation after store recovery
// must clear it.
func TestBackupStalenessAlarmHoldsAFaultThroughAnOutage(t *testing.T) {
	captured := captureBackupAlarmSends(t, nil)
	store, cfg := readBackupStoreBeforeOutage(t)
	claimedAt := backupOutageStart.Add(time.Minute)
	saveBackupAlarmState(context.Background(), cfg, backupAlarmState{
		Alarmed:    map[string]time.Time{backupStalenessRehearsalName: claimedAt},
		Generation: 1,
	})

	store.stop()
	defer store.ensureStarted()
	fixBackupStalenessClock(t, backupOutageStart.Add(3*time.Minute))
	runFreshBackupStalenessCheck(t, cfg)
	alarmed, _ := alarmedBackupMetrics(t, cfg)
	if at, held := alarmed[backupStalenessRehearsalName]; !held || !at.Equal(claimedAt) {
		t.Fatalf("an unreadable rehearsal must hold its fault, state %v", alarmed)
	}

	cfg.BackupS3Endpoint = store.start()
	fixBackupStalenessClock(t, backupOutageStart.Add(4*time.Minute))
	runFreshBackupStalenessCheck(t, cfg)
	alarmed, _ = alarmedBackupMetrics(t, cfg)
	if _, held := alarmed[backupStalenessRehearsalName]; held {
		t.Fatalf("a fresh rehearsal read from the store must clear the fault, state %v", alarmed)
	}
	if len(captured.messages) != 0 {
		t.Fatalf("holding and clearing must not mail, sent %d", len(captured.messages))
	}
}
