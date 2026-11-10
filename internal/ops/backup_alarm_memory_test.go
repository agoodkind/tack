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

// storedDeputyBackupStalenessConfig configures a deputy with a store and a
// ledger without primary runs. Without shared alarm memory, the deputy would
// mail each new fault. TACK-484 recorded that behavior on the QA data guest.
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

// sharedAlarmedBackupMetrics reads the alarm memory shared in store. found
// is false when the shared object does not exist.
func sharedAlarmedBackupMetrics(t *testing.T, store *backupTestStore) (alarmed map[string]time.Time, found bool) {
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

// TestBackupStalenessAlarmSharedMemoryHoldsTheDeputyAfterTheOwnerMails checks
// duplicate suppression across guests. The owner mails and records the fault
// in the shared alarm object. A deputy with a fresh backup root reads that
// record and must not mail the same fault.
func TestBackupStalenessAlarmSharedMemoryHoldsTheDeputyAfterTheOwnerMails(t *testing.T) {
	now := time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	store := newBackupTestStore(t, nil)
	owner := storedBackupStalenessConfig(t, store)
	deputy := storedDeputyBackupStalenessConfig(t, store)

	runStaleBackupStalenessCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf("the owner must mail the fault once, sent %d", len(captured.messages))
	}
	shared, found := sharedAlarmedBackupMetrics(t, store)
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

// TestBackupStalenessAlarmSharedMemoryHoldsTheOwnerAfterTheDeputyMails
// reproduces the order recorded on QA: the deputy mails while the owner is
// down, then the owner starts without local alarm memory. The owner reads the
// deputy's shared record and must not mail the same fault.
func TestBackupStalenessAlarmSharedMemoryHoldsTheOwnerAfterTheDeputyMails(t *testing.T) {
	now := time.Date(2026, 9, 6, 1, 25, 48, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	store := newBackupTestStore(t, nil)
	deputy := storedDeputyBackupStalenessConfig(t, store)
	owner := storedBackupStalenessConfig(t, store)

	logs := runStaleDeputyCheck(t, deputy)
	if len(captured.messages) != 1 {
		t.Fatalf("with no primary run in the window the deputy must mail once, sent %d", len(captured.messages))
	}
	if !strings.Contains(logs, "msg=backup.staleness.primary_not_seen") {
		t.Fatalf("the deputy must have mailed because the primary was not seen:\n%s", logs)
	}
	if shared, found := sharedAlarmedBackupMetrics(t, store); !found || len(shared) != 3 {
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

// TestBackupStalenessAlarmMailsWhenTheSharedMemoryCannotBeRead uses an alarm
// object that the client refuses to read. The run must warn, mail once, and
// record acceptance locally. The next run must use that local record to
// suppress a duplicate despite the unreadable shared object.
func TestBackupStalenessAlarmMailsWhenTheSharedMemoryCannotBeRead(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	store := newBackupTestStore(t, map[string][]byte{
		backupAlarmMemoryKey: bytes.Repeat([]byte("x"), smallObjectMaxBytes+1),
	})
	cfg := storedBackupStalenessConfig(t, store)

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

// TestBackupStalenessAlarmSharedMemoryForgetsAClearFromEitherChecker checks
// recovery recorded by another guest. The owner mails, the deputy observes
// recovery and removes the shared fault, and a later fault must mail again.
func TestBackupStalenessAlarmSharedMemoryForgetsAClearFromEitherChecker(t *testing.T) {
	now := time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	objects := ybExportRunObjects(t, "20260905T230000Z",
		newYBSnapshotManifest("20260905T230000Z", "snap-1", "tack", []string{"yb1"}, ybTestArtifactNames()))
	objects[backupStatusKey(backupStalenessReplicationName)] = marshalBackupStatusMarker(t,
		now.Add(-10*time.Minute), "0 dead nodes, 0 under-replicated tablets")
	store := newBackupTestStore(t, objects)
	owner := storedBackupStalenessConfig(t, store)
	deputy := storedDeputyBackupStalenessConfig(t, store)
	seesHealthyCluster(t, owner, deputy)
	rehearsalKey := backupStatusKey(backupStalenessRehearsalName)

	// The owner's first run observes a rehearsal without a recorded pass.
	runStaleBackupStalenessCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf("the first fault must mail once, sent %d", len(captured.messages))
	}
	if shared, _ := sharedAlarmedBackupMetrics(t, store); len(shared) != 1 || shared[backupStalenessRehearsalName].IsZero() {
		t.Fatalf("the store must remember the rehearsal fault, state = %v", shared)
	}

	// The deputy's second run observes a drill pass and clears the shared fault.
	store.put(rehearsalKey, marshalBackupStatusMarker(t, now.Add(-6*time.Hour), "restore drill passed every leg"))
	var out bytes.Buffer
	if err := RunBackupStalenessCheck(context.Background(), deputy, &out); err != nil {
		t.Fatalf("every mechanism is fresh, so the deputy's check must pass: %v\n%s", err, out.String())
	}
	if len(captured.messages) != 1 {
		t.Fatalf("a clear must mail nothing, sent %d in total", len(captured.messages))
	}
	shared, found := sharedAlarmedBackupMetrics(t, store)
	if !found || len(shared) != 0 {
		t.Fatalf("a clear on the deputy must remove the mechanism from the store, found = %v state = %v", found, shared)
	}

	// The deputy's third run observes the missing marker and mails a new fault.
	store.remove(rehearsalKey)
	runStaleDeputyCheck(t, deputy)
	if len(captured.messages) != 2 {
		t.Fatalf("a second fault after a clear must mail again, sent %d in total", len(captured.messages))
	}

	// The owner's fourth run reads the shared second fault and suppresses mail.
	runStaleBackupStalenessCheck(t, owner)
	if len(captured.messages) != 2 {
		t.Fatalf("the owner must hold the fault the deputy mailed, sent %d in total", len(captured.messages))
	}
}
