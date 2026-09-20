// backup_staleness_blind_guest_test.go proves the staleness check alarms on
// what the guest running it can see. On QA the owner guest was blocked from
// all three ledger masters for 64 minutes and its replication reading still
// said FRESH, because the deputy checker on the data guest kept refreshing the
// shared success marker (TACK-529).

package ops

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// readBackupStatusMarkerObject reads one mechanism's marker straight out of
// the store, so a test can tell what the check left there.
func readBackupStatusMarkerObject(t *testing.T, store *backupTestStore, metric string) backupStatusMarker {
	t.Helper()
	body, found := store.object(backupStatusKey(metric))
	if !found {
		t.Fatalf("no %s marker in the store", metric)
	}
	var marker backupStatusMarker
	if err := json.Unmarshal(body, &marker); err != nil {
		t.Fatalf("decode %s marker %q: %v", metric, body, err)
	}
	return marker
}

// TestBlindGuestAlarmsWhileAnotherGuestKeepsTheMarkerFresh is the QA defect.
// The owner guest sees the cluster once, then hears from no master for 64
// minutes while another guest keeps the shared marker fresh. The owner's
// reading must go stale on its own observation, mail once in words that claim
// only its own blindness, stay silent while it lasts, and clear silently when
// the owner sees the cluster again. The shared marker must survive untouched,
// because the deputy still keeps it.
func TestBlindGuestAlarmsWhileAnotherGuestKeepsTheMarkerFresh(t *testing.T) {
	sawCluster := time.Date(2026, 9, 19, 17, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, sawCluster)
	captured := captureBackupAlarmSends(t, nil)
	objects := ybExportRunObjects(t, "20260919T060000Z",
		newYBSnapshotManifest("20260919T060000Z", "snap-1", "tack", []string{"yb1"}, ybTestArtifactNames()))
	objects[backupStatusKey(backupStalenessRehearsalName)] = marshalBackupStatusMarker(t,
		sawCluster.Add(-6*time.Hour), "restore drill passed every leg")
	store := newBackupTestStore(t, objects)
	owner := storedBackupStalenessConfig(t, store)
	// The leader answers the first probe and every probe after it comes back
	// as a follower's page, the answer that tells this guest to ask a leader
	// it cannot reach. The last body repeats until the bodies run out, so the
	// owner is blind for the two runs in between and sees the cluster again on
	// the last one.
	startYBMasterHealthServer(t, ybHealthAllGood,
		ybFollowerHealthPage, ybFollowerHealthPage, ybHealthAllGood)
	owner.BackupYBMasterAddresses = "[::1]:7100"

	runFreshBackupStalenessCheck(t, owner)
	if len(captured.messages) != 0 {
		t.Fatalf("a guest that sees the cluster must mail nothing, sent %d", len(captured.messages))
	}

	// Another guest keeps the shared marker fresh while this one is blind.
	blindAt := sawCluster.Add(64 * time.Minute)
	deputySawCluster := blindAt.Add(-2 * time.Minute)
	store.put(backupStatusKey(backupStalenessReplicationName),
		marshalBackupStatusMarker(t, deputySawCluster, "0 dead nodes, 0 under-replicated tablets"))
	fixBackupStalenessClock(t, blindAt)

	report := runStaleBackupStalenessCheck(t, owner)
	if !strings.Contains(report, "age=3840s threshold=1800s STALE") {
		t.Fatalf("the blind guest's reading must age from its own observation and read STALE:\n%s", report)
	}
	if !strings.Contains(report, "the shared record dates the cluster healthy at 2026-09-19T18:02:00Z") {
		t.Fatalf("the report must say what the shared record holds:\n%s", report)
	}
	if len(captured.messages) != 1 {
		t.Fatalf("a blind guest must mail once, sent %d", len(captured.messages))
	}
	message := captured.messages[0]
	if message.Subject != "["+backupAlarmHost()+"] This guest cannot see the ledger cluster" {
		t.Errorf("subject = %q", message.Subject)
	}
	wantBody := "This guest cannot reach the ledger cluster (logins and audit trail), " +
		"so it cannot say whether the cluster is healthy. " +
		"It last saw the cluster healthy at 5:00 PM UTC on Sep 19, 2026, 1 hour 4 minutes ago; " +
		"the limit is 30 minutes.\n" +
		"\n" +
		"1. Confirm every ledger guest is up.\n" +
		"2. From this guest, confirm the ledger master page answers.\n" +
		"3. Fix what blocks it; the alarm clears itself once this guest sees the cluster again."
	if message.Body != wantBody {
		t.Errorf("body mismatch:\n got=%q\nwant=%q", message.Body, wantBody)
	}
	for _, claim := range []string{"unhealthy", "never been seen", "under-replicated", "dead nodes"} {
		if strings.Contains(message.Subject+message.Body, claim) {
			t.Errorf("a guest that heard from no master must not claim %q:\n%s", claim, message.Body)
		}
	}
	assertBackupAlarmPlainWords(t, message.Subject, message.Body, owner.BackupS3Endpoint)

	// The shared marker is the deputy's to keep: the blind run neither wrote
	// it nor took its age from it.
	if marker := readBackupStatusMarkerObject(t, store, backupStalenessReplicationName); !marker.At.Equal(deputySawCluster) {
		t.Errorf("the shared marker must still hold the other guest's observation, got %s", marker.At)
	}

	fixBackupStalenessClock(t, blindAt.Add(6*time.Minute))
	runStaleBackupStalenessCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf("a fault that lasts must not mail again, sent %d in total", len(captured.messages))
	}

	fixBackupStalenessClock(t, blindAt.Add(12*time.Minute))
	runFreshBackupStalenessCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf("a guest that sees the cluster again must clear without a mail, sent %d in total", len(captured.messages))
	}
	if alarmed, _ := alarmedBackupMetrics(t, owner); len(alarmed) != 0 {
		t.Fatalf("the cleared fault must be forgotten, state = %v", alarmed)
	}
}
