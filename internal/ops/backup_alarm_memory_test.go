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

// storedDeputyBackupStalenessConfig is a deputy checker over the fake store
// whose ledger holds no primary run, so without the shared memory it would mail
// every new fault the way the data guest did on QA (TACK-484).
func storedDeputyBackupStalenessConfig(t *testing.T, objects map[string][]byte) *config.Config {
	t.Helper()
	cfg := storedBackupStalenessConfig(t, objects)
	cfg.BackupAlarmPrimaryService = primaryService
	cfg.BackupAlarmPrimaryWindowSeconds = 1500
	cfg.BackupAlarmPrimaryGraceSeconds = 90
	skipBackupAlarmGrace(t, func() {})
	installSystemLedger(t)
	return cfg
}

// sharedAlarmedBackupMetrics reads the alarm memory the checkers share from
// the fake store's objects. found is false when no memory has been written.
func sharedAlarmedBackupMetrics(t *testing.T, objects map[string][]byte) (alarmed map[string]time.Time, found bool) {
	t.Helper()
	body, ok := objects[backupAlarmMemoryKey]
	if !ok {
		return nil, false
	}
	var state backupAlarmState
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode shared alarm memory %q: %v", body, err)
	}
	return state.Alarmed, true
}

// TestBackupStalenessAlarmSharedMemoryHoldsTheDeputyAfterTheOwnerMails is the
// one-mail rule across guests through the store: the owner mails a fault and
// records it under backup-status/alarm-state.json, so a deputy with a fresh
// backup root that finds the same fault holds it without a mail.
func TestBackupStalenessAlarmSharedMemoryHoldsTheDeputyAfterTheOwnerMails(t *testing.T) {
	now := time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	objects := map[string][]byte{}
	owner := storedBackupStalenessConfig(t, objects)
	deputy := storedDeputyBackupStalenessConfig(t, objects)

	runStaleBackupStalenessCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf("the owner must mail the fault once, sent %d", len(captured.messages))
	}
	shared, found := sharedAlarmedBackupMetrics(t, objects)
	if !found || len(shared) != 3 {
		t.Fatalf("the accepted mail must be recorded in the store, found = %v state = %v", found, shared)
	}
	for _, name := range []string{backupStalenessExportName, backupStalenessRehearsalName, backupStalenessReplicationName} {
		if !shared[name].Equal(now) {
			t.Errorf("store memory dates %s at %s, want the accept time %s", name, shared[name], now)
		}
	}

	logs := runStaleDeputyCheck(t, deputy)
	if len(captured.messages) != 1 {
		t.Fatalf("a deputy that reads the owner's mail from the store must not mail, sent %d in total", len(captured.messages))
	}
	if !strings.Contains(logs, "msg=backup.staleness.alarm_held") {
		t.Fatalf("the deputy must log the fault as held:\n%s", logs)
	}
	cached, found := readBackupAlarmStateFile(t, deputy)
	if !found || len(cached.Alarmed) != 3 || cached.Generation != 1 {
		t.Fatalf("the deputy must cache the claims it took from the store at the store's generation, found = %v state = %+v", found, cached)
	}
}

// TestBackupStalenessAlarmSharedMemoryHoldsTheOwnerAfterTheDeputyMails is the
// order QA observed: the deputy mails while the owner is down, and the owner
// then comes back with no memory of its own. The owner reads the deputy's mail
// from the store and holds the fault instead of mailing it again.
func TestBackupStalenessAlarmSharedMemoryHoldsTheOwnerAfterTheDeputyMails(t *testing.T) {
	now := time.Date(2026, 9, 6, 1, 25, 48, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	objects := map[string][]byte{}
	deputy := storedDeputyBackupStalenessConfig(t, objects)
	owner := storedBackupStalenessConfig(t, objects)

	logs := runStaleDeputyCheck(t, deputy)
	if len(captured.messages) != 1 {
		t.Fatalf("with no primary run in the window the deputy must mail once, sent %d", len(captured.messages))
	}
	if !strings.Contains(logs, "msg=backup.staleness.primary_not_seen") {
		t.Fatalf("the deputy must have mailed because the primary was not seen:\n%s", logs)
	}
	if shared, found := sharedAlarmedBackupMetrics(t, objects); !found || len(shared) != 3 {
		t.Fatalf("the deputy's mail must be recorded in the store, found = %v state = %v", found, shared)
	}

	fixBackupStalenessClock(t, now.Add(4*time.Minute+16*time.Second))
	logs = runStaleDeputyCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf("an owner that reads the deputy's mail from the store must not mail, sent %d in total", len(captured.messages))
	}
	if !strings.Contains(logs, "msg=backup.staleness.alarm_held") {
		t.Fatalf("the owner must log the fault as held:\n%s", logs)
	}
}

// TestBackupStalenessAlarmMailsWhenTheSharedMemoryCannotBeRead proves the
// store is never a gate on the mail: with an alarm-state object the client
// refuses to read, the run warns, mails once, and records the mail locally;
// the next run on the same guest holds the fault from that file.
func TestBackupStalenessAlarmMailsWhenTheSharedMemoryCannotBeRead(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	objects := map[string][]byte{
		backupAlarmMemoryKey: bytes.Repeat([]byte("x"), smallObjectMaxBytes+1),
	}
	cfg := storedBackupStalenessConfig(t, objects)

	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("an unreadable shared memory must not keep the fault from mailing, sent %d", len(captured.messages))
	}
	if !strings.Contains(logs, "level=WARN msg=backup.staleness.alarm_memory_unreadable") ||
		!strings.Contains(logs, "key="+backupAlarmMemoryKey) ||
		!strings.Contains(logs, "larger than") {
		t.Fatalf("the unreadable memory must be logged at warn with the key and the error:\n%s", logs)
	}
	if alarmed, found := alarmedBackupMetrics(t, cfg); !found || len(alarmed) != 3 {
		t.Fatalf("the accepted mail must be recorded locally, found = %v state = %v", found, alarmed)
	}

	runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("the local file must hold the fault on the next run, sent %d in total", len(captured.messages))
	}
}

// TestBackupStalenessAlarmSharedMemoryForgetsAClearFromEitherChecker proves a
// clear observed on one guest reaches the store: the owner mails the fault,
// the deputy sees the mechanism back and removes it from the shared memory,
// and the mechanism's next fault mails again.
func TestBackupStalenessAlarmSharedMemoryForgetsAClearFromEitherChecker(t *testing.T) {
	now := time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	objects := fakeYBExportRunObjects(t, "20260905T230000Z",
		newYBSnapshotManifest("20260905T230000Z", "snap-1", "tack", []string{"yb1"}, ybTestArtifactNames()))
	objects[backupStatusKey(backupStalenessReplicationName)] = marshalBackupStatusMarker(t,
		now.Add(-10*time.Minute), "0 dead nodes, 0 under-replicated tablets")
	owner := storedBackupStalenessConfig(t, objects)
	deputy := storedDeputyBackupStalenessConfig(t, objects)
	rehearsalKey := backupStatusKey(backupStalenessRehearsalName)

	// Run 1, owner: the rehearsal has never passed.
	runStaleBackupStalenessCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf("the first fault must mail once, sent %d", len(captured.messages))
	}
	if shared, _ := sharedAlarmedBackupMetrics(t, objects); len(shared) != 1 || shared[backupStalenessRehearsalName].IsZero() {
		t.Fatalf("the store must remember the rehearsal fault, state = %v", shared)
	}

	// Run 2, deputy: the drill passed, so the deputy clears the fault in the store.
	objects[rehearsalKey] = marshalBackupStatusMarker(t, now.Add(-6*time.Hour), "restore drill passed every leg")
	var out bytes.Buffer
	if err := RunBackupStalenessCheck(context.Background(), deputy, &out); err != nil {
		t.Fatalf("every mechanism is fresh, so the deputy's check must pass: %v\n%s", err, out.String())
	}
	if len(captured.messages) != 1 {
		t.Fatalf("a clear must mail nothing, sent %d in total", len(captured.messages))
	}
	shared, found := sharedAlarmedBackupMetrics(t, objects)
	if !found || len(shared) != 0 {
		t.Fatalf("a clear on the deputy must remove the mechanism from the store, found = %v state = %v", found, shared)
	}

	// Run 3, deputy: the marker is gone again, a second fault, which mails.
	delete(objects, rehearsalKey)
	runStaleDeputyCheck(t, deputy)
	if len(captured.messages) != 2 {
		t.Fatalf("a second fault after a clear must mail again, sent %d in total", len(captured.messages))
	}

	// Run 4, owner: the second fault is already in the store, so it holds.
	runStaleBackupStalenessCheck(t, owner)
	if len(captured.messages) != 2 {
		t.Fatalf("the owner must hold the fault the deputy mailed, sent %d in total", len(captured.messages))
	}
}
