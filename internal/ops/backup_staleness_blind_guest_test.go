// These tests check that staleness alarms use the checking guest's own
// observations. The TACK-529 account records a QA incident:
//
//   - The owner guest could not contact any of the three ledger masters for
//     64 minutes.
//   - Its replication reading still reported FRESH.
//   - The deputy checker on the data guest refreshed the shared success
//     marker during the owner's failed observations.

package ops

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

// seesHealthyCluster configures the supplied ledger master addresses to use
// a server that reports healthy. Tests of other mechanisms require a usable
// local cluster observation to avoid an alarm about the checking guest's
// failed cluster probe.
func seesHealthyCluster(t *testing.T, cfgs ...*config.Config) {
	t.Helper()
	startYBMasterHealthServer(t, ybHealthAllGood)
	for _, cfg := range cfgs {
		cfg.BackupYBMasterAddresses = "[::1]:7100"
	}
}

// readBackupStatusMarkerObject reads a mechanism's stored marker for tests
// that inspect the check's writes.
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

// TestBlindGuestAlarmsWhileAnotherGuestKeepsTheMarkerFresh reproduces the
// TACK-529 QA defect. The owner first observes a healthy cluster. Another
// guest refreshes the shared marker while the owner cannot obtain a usable
// master response for 64 minutes.
//
// The test checks these outcomes:
//
//   - The owner's observation becomes STALE with age=3840s and
//     threshold=1800s.
//   - The initial stale check produces one alarm message about this guest's
//     inability to observe the cluster.
//   - Continued failure keeps the message count at one.
//   - The blind run preserves the deputy's shared-marker timestamp,
//     2026-09-19T18:02:00Z, without dating its own observation from it.
//   - A healthy probe clears alarm state without sending another message.
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
	// The server returns a healthy leader response, two follower pages, and
	// another healthy response. The follower pages do not provide health data,
	// and the configured probe does not obtain a usable leader response during
	// the middle runs and observes health again on the last run. The server
	// repeats its final response after the configured responses run out.
	startYBMasterHealthServer(t, ybHealthAllGood,
		ybFollowerHealthPage, ybFollowerHealthPage, ybHealthAllGood)
	owner.BackupYBMasterAddresses = "[::1]:7100"

	runFreshBackupStalenessCheck(t, owner)
	if len(captured.messages) != 0 {
		t.Fatalf("a guest that sees the cluster must mail nothing, sent %d", len(captured.messages))
	}

	// Another guest refreshes the shared marker while the owner cannot observe
	// the cluster.
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

	// The blind run must preserve the deputy's marker and must not use that
	// marker's timestamp to date its own observation.
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

// TestDegradedClusterStillSaysTheClusterIsUnhealthy checks mail after a
// master reports missing replicas. The message must report cluster
// degradation and date the last healthy state from this guest's own
// observation.
func TestDegradedClusterStillSaysTheClusterIsUnhealthy(t *testing.T) {
	sawCluster := time.Date(2026, 9, 19, 17, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, sawCluster)
	captured := captureBackupAlarmSends(t, nil)
	objects := ybExportRunObjects(t, "20260919T060000Z",
		newYBSnapshotManifest("20260919T060000Z", "snap-1", "tack", []string{"yb1"}, ybTestArtifactNames()))
	objects[backupStatusKey(backupStalenessRehearsalName)] = marshalBackupStatusMarker(t,
		sawCluster.Add(-6*time.Hour), "restore drill passed every leg")
	owner := storedBackupStalenessConfig(t, newBackupTestStore(t, objects))
	startYBMasterHealthServer(t, ybHealthAllGood,
		`{"dead_nodes":["7ba1"],"most_recent_uptime":9,"under_replicated_tablets":["t1"]}`)
	owner.BackupYBMasterAddresses = "[::1]:7100"

	runFreshBackupStalenessCheck(t, owner)
	fixBackupStalenessClock(t, sawCluster.Add(45*time.Minute))
	runStaleBackupStalenessCheck(t, owner)

	if len(captured.messages) != 1 {
		t.Fatalf("a degraded cluster must mail once, sent %d", len(captured.messages))
	}
	message := captured.messages[0]
	if message.Subject != "["+backupAlarmHost()+"] Ledger cluster unhealthy for 45 minutes" {
		t.Errorf("subject = %q", message.Subject)
	}
	if !strings.Contains(message.Body, "The ledger cluster (logins and audit trail) was last healthy at "+
		"5:00 PM UTC on Sep 19, 2026, 45 minutes ago; the limit is 30 minutes. "+
		"The last check reported: 1 dead nodes, 1 under-replicated tablets.") {
		t.Errorf("body mismatch:\n%s", message.Body)
	}
	assertBackupAlarmPlainWords(t, message.Subject, message.Body, owner.BackupS3Endpoint)
}
