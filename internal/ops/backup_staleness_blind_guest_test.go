// Per-guest replication tests cover TACK-529: a fresh shared observation from
// another guest must not conceal this checker's prolonged inability to read
// usable master health reports.

package ops

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

// seesHealthyCluster supplies a healthy local master response for tests of
// other backup mechanisms that must not trigger a replication alarm.
func seesHealthyCluster(t *testing.T, cfgs ...*config.Config) {
	t.Helper()
	startYBMasterHealthServer(t, ybHealthAllGood)

	for _, cfg := range cfgs {
		cfg.BackupYBMasterAddresses = "[::1]:7100"
	}
}

// readBackupStatusMarkerObject decodes the actual marker stored by a check.
func readBackupStatusMarkerObject(
	t *testing.T,
	store *backupTestStore,
	metric string,
) backupStatusMarker {
	t.Helper()
	body, found := store.object(backupStatusKey(metric))
	if !found {
		t.Fatalf("the store does not contain a %s marker", metric)
	}

	var marker backupStatusMarker
	if err := json.Unmarshal(body, &marker); err != nil {
		t.Fatalf("decode %s marker %q: %v", metric, body, err)
	}

	return marker
}

// TestGuestAlarmsWithoutLocalHealthDespiteFreshSharedMarker checks local aging,
// one observation-failure email, duplicate suppression, and silent recovery.
// Another guest's shared healthy marker must remain unchanged throughout.
func TestGuestAlarmsWithoutLocalHealthDespiteFreshSharedMarker(t *testing.T) {
	sawCluster := time.Date(2026, 9, 19, 17, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, sawCluster)
	captured := captureBackupAlarmSends(t, nil)
	objects := ybExportRunObjects(
		t,
		"20260919T060000Z",
		newYBSnapshotManifest(
			"20260919T060000Z",
			"snap-1",
			"tack",
			[]string{"yb1"},
			ybTestArtifactNames(),
		),
	)
	objects[backupStatusKey(backupStalenessRehearsalName)] = marshalBackupStatusMarker(
		t,
		sawCluster.Add(-6*time.Hour),
		"restore drill passed every leg",
	)
	store := newBackupTestStore(t, objects)
	owner := storedBackupStalenessConfig(t, store)
	// The first probe establishes health. Two follower responses omit usable
	// cluster health before the final healthy response establishes recovery.
	startYBMasterHealthServer(
		t,
		ybHealthAllGood,
		ybFollowerHealthPage,
		ybFollowerHealthPage,
		ybHealthAllGood,
	)
	owner.BackupYBMasterAddresses = "[::1]:7100"

	runFreshBackupStalenessCheck(t, owner)
	if len(captured.messages) != 0 {
		t.Fatalf("healthy local observations must not send email, sent %d", len(captured.messages))
	}

	// Another guest refreshes shared state without this guest observing health.
	blindAt := sawCluster.Add(64 * time.Minute)
	deputySawCluster := blindAt.Add(-2 * time.Minute)
	store.put(
		backupStatusKey(backupStalenessReplicationName),
		marshalBackupStatusMarker(t, deputySawCluster, "0 dead nodes, 0 under-replicated tablets"),
	)
	fixBackupStalenessClock(t, blindAt)

	report := runStaleBackupStalenessCheck(t, owner)
	if !strings.Contains(report, "age=3840s threshold=1800s STALE") {
		t.Fatalf(
			"this guest's metric must use its own observation age and report STALE:\n%s",
			report,
		)
	}

	if !strings.Contains(
		report,
		"the shared record dates the cluster healthy at 2026-09-19T18:02:00Z",
	) {
		t.Fatalf("the report must include the shared observation separately:\n%s", report)
	}

	if len(captured.messages) != 1 {
		t.Fatalf(
			"prolonged local observation failure must email once, sent %d",
			len(captured.messages),
		)
	}

	message := captured.messages[0]
	if message.Subject != "["+backupAlarmHost()+"] Backup check reported a problem" {
		t.Errorf("subject = %q", message.Subject)
	}

	wantBody := "replication age=3840s threshold=1800s STALE no master answered the health check; " +
		"the shared record dates the cluster healthy at 2026-09-19T18:02:00Z\n"
	if message.Body != wantBody {
		t.Errorf("body mismatch:\n got=%q\nwant=%q", message.Body, wantBody)
	}

	for _, claim := range []string{"unhealthy", "never been seen", "under-replicated", "dead nodes"} {
		if strings.Contains(message.Subject+message.Body, claim) {
			t.Errorf("failed master observations must not establish %q:\n%s", claim, message.Body)
		}
	}

	// Failed local observation must not replace the other guest's shared marker.
	if marker := readBackupStatusMarkerObject(
		t,
		store,
		backupStalenessReplicationName,
	); !marker.At.Equal(deputySawCluster) {
		t.Errorf("the shared marker must retain the other guest's observation, got %s", marker.At)
	}

	fixBackupStalenessClock(t, blindAt.Add(6*time.Minute))
	runStaleBackupStalenessCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf("a continuing fault must not repeat email, sent %d total", len(captured.messages))
	}

	fixBackupStalenessClock(t, blindAt.Add(12*time.Minute))
	runFreshBackupStalenessCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"healthy recovery must clear the fault without email, sent %d total",
			len(captured.messages),
		)
	}

	if alarmed, _ := alarmedBackupMetrics(t, owner); len(alarmed) != 0 {
		t.Fatalf("recovery must remove recorded faults, state = %v", alarmed)
	}
}

// TestDegradedClusterReportsObservedReplicationFailure checks a valid master's
// degraded answer and an age based on this guest's prior healthy observation.
func TestDegradedClusterReportsObservedReplicationFailure(t *testing.T) {
	sawCluster := time.Date(2026, 9, 19, 17, 0, 0, 0, time.UTC)
	fixBackupStalenessClock(t, sawCluster)
	captured := captureBackupAlarmSends(t, nil)
	objects := ybExportRunObjects(
		t,
		"20260919T060000Z",
		newYBSnapshotManifest(
			"20260919T060000Z",
			"snap-1",
			"tack",
			[]string{"yb1"},
			ybTestArtifactNames(),
		),
	)
	objects[backupStatusKey(backupStalenessRehearsalName)] = marshalBackupStatusMarker(
		t,
		sawCluster.Add(-6*time.Hour),
		"restore drill passed every leg",
	)
	owner := storedBackupStalenessConfig(t, newBackupTestStore(t, objects))
	startYBMasterHealthServer(
		t,
		ybHealthAllGood,
		`{"dead_nodes":["7ba1"],"most_recent_uptime":9,"under_replicated_tablets":["t1"]}`,
	)
	owner.BackupYBMasterAddresses = "[::1]:7100"

	runFreshBackupStalenessCheck(t, owner)
	fixBackupStalenessClock(t, sawCluster.Add(45*time.Minute))
	runStaleBackupStalenessCheck(t, owner)

	if len(captured.messages) != 1 {
		t.Fatalf("a degraded cluster must email once, sent %d", len(captured.messages))
	}

	message := captured.messages[0]
	if message.Subject != "["+backupAlarmHost()+"] Backup check reported a problem" {
		t.Errorf("subject = %q", message.Subject)
	}

	if !strings.Contains(
		message.Body,
		"replication age=2700s threshold=1800s STALE 1 dead nodes, 1 under-replicated tablets",
	) {
		t.Errorf("body mismatch:\n%s", message.Body)
	}
}
