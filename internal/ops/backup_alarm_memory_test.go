package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

// storedDeputyBackupStalenessConfig configures a deputy with an audit ledger
// without recent primary activity. Only shared alarm state can suppress its
// duplicate fault email in these tests (TACK-484).
func storedDeputyBackupStalenessConfig(t *testing.T, store *backupTestStore) *config.Config {
	t.Helper()
	cfg := storedBackupStalenessConfig(t, store)
	cfg.BackupAlarmPrimaryService = primaryService
	cfg.BackupAlarmPrimaryWindowSeconds = 1500
	cfg.BackupAlarmPrimaryGraceSeconds = 90
	skipBackupAlarmGrace(t, func() {})
	installSystemLedger(t)

	return cfg
}

// sharedAlarmedBackupMetrics reads shared fault acceptance times and reports
// found=false when the state object does not exist.
func sharedAlarmedBackupMetrics(
	t *testing.T,
	store *backupTestStore,
) (alarmed map[string]time.Time, found bool) {
	t.Helper()
	body, ok := store.object(backupAlarmMemoryKey)
	if !ok {
		return nil, false
	}

	var state backupAlarmState
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode shared alarm memory %q: %v", body, err)
	}

	return state.Alarmed, true
}

// TestBackupAlarmSharedStateSuppressesDeputyDuplicateEmail checks shared
// acceptance timestamps and local caching after the owner's first fault email.
func TestBackupAlarmSharedStateSuppressesDeputyDuplicateEmail(t *testing.T) {
	now := time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	store := newBackupTestStore(t, nil)
	owner := storedBackupStalenessConfig(t, store)
	deputy := storedDeputyBackupStalenessConfig(t, store)

	runStaleBackupStalenessCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf("the owner must email the fault once, sent %d", len(captured.messages))
	}

	shared, found := sharedAlarmedBackupMetrics(t, store)
	if !found || len(shared) != 3 {
		t.Fatalf(
			"shared state must record the accepted email, found = %v state = %v",
			found,
			shared,
		)
	}

	for _, name := range []string{
		backupStalenessExportName,
		backupStalenessRehearsalName,
		backupStalenessReplicationName,
	} {
		if !shared[name].Equal(now) {
			t.Errorf(
				"store memory dates %s at %s, want the accept time %s",
				name,
				shared[name],
				now,
			)
		}
	}

	logs := runStaleDeputyCheck(t, deputy)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"shared owner state must suppress duplicate deputy email, sent %d total",
			len(captured.messages),
		)
	}

	if !strings.Contains(logs, "msg=backup.staleness.alarm_held") {
		t.Fatalf("the deputy must log alarm suppression:\n%s", logs)
	}

	cached, found := readBackupAlarmStateFile(t, deputy)
	if !found || len(cached.Alarmed) != 3 || cached.Generation != 1 {
		t.Fatalf(
			"the deputy must cache shared faults at the shared generation, found = %v state = %+v",
			found,
			cached,
		)
	}
}

// TestBackupAlarmSharedStateSuppressesOwnerDuplicateEmail checks that owner
// recovery after a deputy email does not produce a duplicate fault email.
func TestBackupAlarmSharedStateSuppressesOwnerDuplicateEmail(t *testing.T) {
	now := time.Date(2026, 9, 6, 1, 25, 48, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	store := newBackupTestStore(t, nil)
	deputy := storedDeputyBackupStalenessConfig(t, store)
	owner := storedBackupStalenessConfig(t, store)

	logs := runStaleDeputyCheck(t, deputy)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"the deputy must email once without a recent primary run, sent %d",
			len(captured.messages),
		)
	}

	if !strings.Contains(logs, "msg=backup.staleness.primary_not_seen") {
		t.Fatalf("the deputy must log missing primary activity:\n%s", logs)
	}

	if shared, found := sharedAlarmedBackupMetrics(t, store); !found || len(shared) != 3 {
		t.Fatalf(
			"shared state must record the deputy's email, found = %v state = %v",
			found,
			shared,
		)
	}

	fixBackupStalenessClock(t, now.Add(4*time.Minute+16*time.Second))
	logs = runStaleDeputyCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"shared deputy state must suppress duplicate owner email, sent %d total",
			len(captured.messages),
		)
	}

	if !strings.Contains(logs, "msg=backup.staleness.alarm_held") {
		t.Fatalf("the owner must log alarm suppression:\n%s", logs)
	}
}

// TestBackupStalenessAlarmMailsWhenTheSharedMemoryCannotBeRead checks email
// delivery and local state persistence despite an unreadable shared object.
func TestBackupStalenessAlarmMailsWhenTheSharedMemoryCannotBeRead(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	store := newBackupTestStore(
		t,
		map[string][]byte{
			backupAlarmMemoryKey: bytes.Repeat([]byte("x"), smallObjectMaxBytes+1),
		},
	)
	cfg := storedBackupStalenessConfig(t, store)

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"unreadable shared state must not prevent fault email, sent %d",
			len(captured.messages),
		)
	}

	if !strings.Contains(logs, "level=WARN msg=backup.staleness.alarm_memory_unreadable") ||
		!strings.Contains(logs, "key="+backupAlarmMemoryKey) ||
		!strings.Contains(logs, "larger than") {
		t.Fatalf("the warning must include the unreadable state key and error:\n%s", logs)
	}

	if alarmed, found := alarmedBackupMetrics(t, cfg); !found || len(alarmed) != 3 {
		t.Fatalf(
			"local state must record the accepted email, found = %v state = %v",
			found,
			alarmed,
		)
	}

	runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"local state must suppress duplicate email on the next run, sent %d total",
			len(captured.messages),
		)
	}
}

// TestBackupAlarmSharedStateAllowsEmailAfterRecovery checks shared recovery
// removal followed by another fault and duplicate suppression across guests.
func TestBackupAlarmSharedStateAllowsEmailAfterRecovery(t *testing.T) {
	now := time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	objects := ybExportRunObjects(
		t,
		"20260905T230000Z",
		newYBSnapshotManifest(
			"20260905T230000Z",
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
	owner := storedBackupStalenessConfig(t, store)
	deputy := storedDeputyBackupStalenessConfig(t, store)
	seesHealthyCluster(t, owner, deputy)
	rehearsalKey := backupStatusKey(backupStalenessRehearsalName)

	// The owner observes the first rehearsal fault.
	runStaleBackupStalenessCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf("the first fault must email once, sent %d", len(captured.messages))
	}

	if shared, _ := sharedAlarmedBackupMetrics(t, store); len(shared) != 1 ||
		shared[backupStalenessRehearsalName].IsZero() {
		t.Fatalf("shared state must record the rehearsal fault, state = %v", shared)
	}

	// The deputy observes successful rehearsal and removes the shared fault.
	store.put(
		rehearsalKey,
		marshalBackupStatusMarker(t, now.Add(-6*time.Hour), "restore drill passed every leg"),
	)
	var out bytes.Buffer
	if err := RunBackupStalenessCheck(context.Background(), deputy, &out); err != nil {
		t.Fatalf(
			"the deputy's check must pass with every mechanism fresh: %v\n%s",
			err,
			out.String(),
		)
	}

	if len(captured.messages) != 1 {
		t.Fatalf("recovery must not send email, sent %d total", len(captured.messages))
	}

	shared, found := sharedAlarmedBackupMetrics(t, store)
	if !found || len(shared) != 0 {
		t.Fatalf(
			"deputy recovery must remove the shared fault, found = %v state = %v",
			found,
			shared,
		)
	}

	// Removing the marker creates a second fault for the deputy.
	store.remove(rehearsalKey)
	runStaleDeputyCheck(t, deputy)
	if len(captured.messages) != 2 {
		t.Fatalf(
			"a second fault after recovery must email again, sent %d total",
			len(captured.messages),
		)
	}

	// Shared state suppresses the owner's duplicate second-fault email.
	runStaleBackupStalenessCheck(t, owner)
	if len(captured.messages) != 2 {
		t.Fatalf(
			"the owner must not duplicate the deputy's second email, sent %d total",
			len(captured.messages),
		)
	}
}
