package ops

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

// unansweredObjectStoreEndpoint is an object-store address nothing listens
// on, the shape of a store whose guest is stopped.
const unansweredObjectStoreEndpoint = "http://[::1]:1"

// backupOutageStart is when the guest last read the store before it stopped
// answering.
var backupOutageStart = time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

// readBackupStoreBeforeOutage runs the command once against a store where
// every mechanism is fresh: the export finished 35 hours 50 minutes ago
// against a 36 hour limit, the rehearsal passed a day ago, and the cluster
// was last recorded healthy 5 minutes ago against a 30 minute limit. It
// returns the config, still pointed at that store.
func readBackupStoreBeforeOutage(t *testing.T) *config.Config {
	t.Helper()
	fixBackupStalenessClock(t, backupOutageStart)
	objects := fakeYBExportRunObjects(t, "20260828T001000Z",
		newYBSnapshotManifest("20260828T001000Z", "snap-1", "tack", []string{"yb1"}, ybTestArtifactNames()))
	objects[backupStatusKey(backupStalenessRehearsalName)] = marshalBackupStatusMarker(t,
		backupOutageStart.Add(-24*time.Hour), "restore drill passed every leg")
	objects[backupStatusKey(backupStalenessReplicationName)] = marshalBackupStatusMarker(t,
		backupOutageStart.Add(-5*time.Minute), "0 dead nodes, 0 under-replicated tablets")
	cfg := storedBackupStalenessConfig(t, objects)
	runFreshBackupStalenessCheck(t, cfg)
	return cfg
}

// runFreshBackupStalenessCheck runs the command once and asserts it found
// nothing stale, and returns the report.
func runFreshBackupStalenessCheck(t *testing.T, cfg *config.Config) string {
	t.Helper()
	var out bytes.Buffer
	if err := RunBackupStalenessCheck(context.Background(), cfg, &out); err != nil {
		t.Fatalf("the run must find nothing stale: %v\n%s", err, out.String())
	}
	return out.String()
}

// TestBackupStalenessAlarmStaysQuietThroughAShortStoreOutage is the QA mail
// of 2026-09-19: the store's guest stopped minutes after a run that read
// every mechanism fresh. Each reading is then dated from that run, every
// mechanism is still inside its limit, and the run mails nothing and exits
// zero while the report still says the readings could not be taken.
func TestBackupStalenessAlarmStaysQuietThroughAShortStoreOutage(t *testing.T) {
	captured := captureBackupAlarmSends(t, nil)
	cfg := readBackupStoreBeforeOutage(t)

	cfg.BackupS3Endpoint = unansweredObjectStoreEndpoint
	fixBackupStalenessClock(t, backupOutageStart.Add(3*time.Minute))
	report := runFreshBackupStalenessCheck(t, cfg)

	if len(captured.messages) != 0 {
		t.Fatalf("an outage inside every limit must not mail, sent %q", captured.messages[0].Subject)
	}
	if strings.Count(report, "unreadable, dated from the reading at 2026-08-29T12:00:00Z") != 3 {
		t.Errorf("the report must date each unreadable reading from the last run:\n%s", report)
	}
}

// TestBackupStalenessAlarmMailsTheObjectStoreOnceAfterTheThreshold keeps the
// store down until two mechanisms pass their limits counted from the last
// reading. That run mails once, naming the store in one paragraph with the
// time this guest last read it and each mechanism in plain words with none
// of the client's error text; a later run while the outage lasts mails nothing.
func TestBackupStalenessAlarmMailsTheObjectStoreOnceAfterTheThreshold(t *testing.T) {
	captured := captureBackupAlarmSends(t, nil)
	cfg := readBackupStoreBeforeOutage(t)
	cfg.BackupS3Endpoint = unansweredObjectStoreEndpoint
	fixBackupStalenessClock(t, backupOutageStart.Add(3*time.Minute))
	runFreshBackupStalenessCheck(t, cfg)

	fixBackupStalenessClock(t, backupOutageStart.Add(26*time.Minute))
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
		"was made at 12:10 AM UTC on Aug 28, 2026, 36 hours 16 minutes ago; the limit is 36 hours.\n" +
		"1. On the owner guest, run journalctl -u tack-ledger-export.\n" +
		"2. On each data guest, run journalctl -u tack-ledger-archive.\n" +
		"3. Confirm the object store accepts writes, then run systemctl start tack-ledger-export.\n" +
		"\n" +
		"Ledger cluster health status could not be read\n" +
		"The ledger cluster's last healthy reading could not be read. This guest last recorded it healthy " +
		"at 11:55 AM UTC on Aug 29, 2026, 31 minutes ago; the limit is 30 minutes.\n" +
		"1. Confirm every ledger guest is up.\n" +
		"2. On the owner guest, confirm every node is alive on the ledger master page.\n" +
		"3. Wait for tablets to re-copy; the alarm clears itself once the cluster is healthy."
	if message.Body != wantBody {
		t.Errorf("body mismatch:\n got=%q\nwant=%q", message.Body, wantBody)
	}
	for _, raw := range []string{"operation error", "StatusCode", "http", "[::1]", "request send failed"} {
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

// TestBackupStalenessAlarmHoldsAFaultThroughAnOutage proves a reading dated
// from an earlier one never clears a fault. The guest holds a rehearsal fault
// the other checker mailed, the store stops answering, and the rehearsal's
// last reading is still fresh: the fault stays held. Once the store answers
// with the rehearsal fresh, the fault clears.
func TestBackupStalenessAlarmHoldsAFaultThroughAnOutage(t *testing.T) {
	captured := captureBackupAlarmSends(t, nil)
	cfg := readBackupStoreBeforeOutage(t)
	storeEndpoint := cfg.BackupS3Endpoint
	claimedAt := backupOutageStart.Add(time.Minute)
	saveBackupAlarmState(context.Background(), cfg, backupAlarmState{
		Alarmed:    map[string]time.Time{backupStalenessRehearsalName: claimedAt},
		Generation: 1,
	})

	cfg.BackupS3Endpoint = unansweredObjectStoreEndpoint
	fixBackupStalenessClock(t, backupOutageStart.Add(3*time.Minute))
	runFreshBackupStalenessCheck(t, cfg)
	alarmed, _ := alarmedBackupMetrics(t, cfg)
	if at, held := alarmed[backupStalenessRehearsalName]; !held || !at.Equal(claimedAt) {
		t.Fatalf("an unreadable rehearsal must hold its fault, state %v", alarmed)
	}

	cfg.BackupS3Endpoint = storeEndpoint
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
