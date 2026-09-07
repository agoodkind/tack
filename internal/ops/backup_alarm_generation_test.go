package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

// readBackupAlarmStateFile reads a guest's whole state file, generation
// included. found is false when no file exists.
func readBackupAlarmStateFile(t *testing.T, cfg *config.Config) (state backupAlarmState, found bool) {
	t.Helper()
	body, err := os.ReadFile(backupAlarmStatePath(cfg))
	if errors.Is(err, os.ErrNotExist) {
		return state, false
	}
	if err != nil {
		t.Fatalf("read alarm state: %v", err)
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode alarm state %q: %v", body, err)
	}
	return state, true
}

// readSharedBackupAlarmMemory reads the whole shared memory object from the
// fake store's objects, generation included. found is false when absent.
func readSharedBackupAlarmMemory(t *testing.T, objects map[string][]byte) (state backupAlarmState, found bool) {
	t.Helper()
	body, ok := objects[backupAlarmMemoryKey]
	if !ok {
		return state, false
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode shared alarm memory %q: %v", body, err)
	}
	return state, true
}

// rehearsalFaultObjects is a store where only the restore rehearsal is at
// fault: a fresh export run and a fresh replication marker, no rehearsal.
func rehearsalFaultObjects(t *testing.T, now time.Time) map[string][]byte {
	t.Helper()
	objects := fakeYBExportRunObjects(t, "20260905T230000Z",
		newYBSnapshotManifest("20260905T230000Z", "snap-1", "tack", []string{"yb1"}, ybTestArtifactNames()))
	objects[backupStatusKey(backupStalenessReplicationName)] = marshalBackupStatusMarker(t,
		now.Add(-10*time.Minute), "0 dead nodes, 0 under-replicated tablets")
	return objects
}

// TestBackupStalenessAlarmAdoptsAClearTheOtherCheckerRecorded is the case a
// union cannot carry: the owner mails the rehearsal fault, only the deputy
// sees the drill pass and removes the fault from the store, and the owner's
// file still holds it when the drill fails again. The store's generation is
// past the file's, so the owner adopts the store and mails the new fault.
func TestBackupStalenessAlarmAdoptsAClearTheOtherCheckerRecorded(t *testing.T) {
	now := time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	objects := rehearsalFaultObjects(t, now)
	owner := storedBackupStalenessConfig(t, objects)
	deputy := storedDeputyBackupStalenessConfig(t, objects)
	rehearsalKey := backupStatusKey(backupStalenessRehearsalName)

	runStaleBackupStalenessCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf("the first fault must mail once, sent %d", len(captured.messages))
	}
	ownerFile, _ := readBackupAlarmStateFile(t, owner)
	if ownerFile.Generation != 1 || len(ownerFile.Alarmed) != 1 {
		t.Fatalf("the owner's file must sync with the store at generation 1, got %+v", ownerFile)
	}

	objects[rehearsalKey] = marshalBackupStatusMarker(t, now.Add(-6*time.Hour), "restore drill passed every leg")
	var out bytes.Buffer
	if err := RunBackupStalenessCheck(context.Background(), deputy, &out); err != nil {
		t.Fatalf("every mechanism is fresh, so the deputy's check must pass: %v\n%s", err, out.String())
	}
	shared, _ := readSharedBackupAlarmMemory(t, objects)
	if shared.Generation != 2 || len(shared.Alarmed) != 0 {
		t.Fatalf("the deputy's clear must advance the store to generation 2 with nothing alarmed, got %+v", shared)
	}

	delete(objects, rehearsalKey)
	logs := runStaleDeputyCheck(t, owner)
	if len(captured.messages) != 2 {
		t.Fatalf("the owner must adopt the clear from the store and mail the second fault, sent %d in total", len(captured.messages))
	}
	if !strings.Contains(logs, "msg=backup.staleness.alarm_memory_adopted") ||
		!strings.Contains(logs, "generation=2 local_generation=1") {
		t.Fatalf("the owner must log that it adopted the newer store copy:\n%s", logs)
	}
	shared, _ = readSharedBackupAlarmMemory(t, objects)
	ownerFile, _ = readBackupAlarmStateFile(t, owner)
	if shared.Generation != 3 || ownerFile.Generation != 3 || len(shared.Alarmed) != 1 {
		t.Fatalf("the second mail must land in the store at generation 3 and sync the file, store %+v file %+v", shared, ownerFile)
	}
}

// TestBackupStalenessAlarmHoldsFromTheCacheWhenTheStoreTurnsUnreadable proves
// a held fault is cached: the owner mails, the deputy holds the fault it read
// from the store, and when the deputy's next read fails the fault is still in
// its own file, so it holds rather than mailing the fault again.
func TestBackupStalenessAlarmHoldsFromTheCacheWhenTheStoreTurnsUnreadable(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	objects := map[string][]byte{}
	owner := storedBackupStalenessConfig(t, objects)
	deputy := storedDeputyBackupStalenessConfig(t, objects)

	runStaleBackupStalenessCheck(t, owner)
	runStaleDeputyCheck(t, deputy)
	if len(captured.messages) != 1 {
		t.Fatalf("the owner mails once and the deputy holds, sent %d in total", len(captured.messages))
	}
	cached, found := readBackupAlarmStateFile(t, deputy)
	if !found || len(cached.Alarmed) != 3 || cached.Generation != 1 {
		t.Fatalf("the deputy must cache the held claims at the store's generation, found = %v state = %+v", found, cached)
	}

	objects[backupAlarmMemoryKey] = bytes.Repeat([]byte("x"), smallObjectMaxBytes+1)
	logs := runStaleDeputyCheck(t, deputy)
	if len(captured.messages) != 1 {
		t.Fatalf("a deputy that cached the claims must hold them while the store is unreadable, sent %d in total", len(captured.messages))
	}
	if !strings.Contains(logs, "msg=backup.staleness.alarm_memory_unreadable") ||
		!strings.Contains(logs, "msg=backup.staleness.alarm_held") {
		t.Fatalf("the unreadable store and the held fault must both be logged:\n%s", logs)
	}
}

// TestBackupStalenessAlarmWritesTheStoreOnTheRunAfterAFailedPut covers a
// store that serves reads but refuses writes: the mail is recorded in the
// file at the store's old generation, and the next run, with the store
// accepting writes again and still without the fault, unions the file back
// in, writes the store, and mails nothing.
func TestBackupStalenessAlarmWritesTheStoreOnTheRunAfterAFailedPut(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	objects := map[string][]byte{}
	store, _, cfg := startFakeBackupObjectStore(t, "tack-backups", objects)
	cfg.BackupRoot = t.TempDir()
	cfg.BackupYBMasterAddresses = "127.0.0.1:7100"
	cfg.BackupStalenessExportMaxSeconds = 129600
	cfg.BackupStalenessRehearsalMaxSeconds = 691200
	cfg.BackupStalenessReplicationMaxSeconds = 1800
	cfg.BackupAlarmEmail = "backups@example.test"

	store.refusePut = true
	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("the fault must mail once whatever the store does with the record, sent %d", len(captured.messages))
	}
	if !strings.Contains(logs, "level=ERROR msg=backup.staleness.alarm_memory_write_failed") ||
		!strings.Contains(logs, "AccessDenied") {
		t.Fatalf("the refused put must be logged at error with its cause:\n%s", logs)
	}
	if _, found := readSharedBackupAlarmMemory(t, objects); found {
		t.Fatal("a refused put must leave no memory object in the store")
	}
	file, found := readBackupAlarmStateFile(t, cfg)
	if !found || len(file.Alarmed) != 3 || file.Generation != 0 {
		t.Fatalf("the file must record the mail at the store's old generation, found = %v state = %+v", found, file)
	}

	store.refusePut = false
	runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("the next run must hold the fault from the file, sent %d in total", len(captured.messages))
	}
	shared, found := readSharedBackupAlarmMemory(t, objects)
	if !found || len(shared.Alarmed) != 3 || shared.Generation != 1 {
		t.Fatalf("the next run must write the unioned file into the store at generation 1, found = %v state = %+v", found, shared)
	}
	if file, _ := readBackupAlarmStateFile(t, cfg); file.Generation != 1 {
		t.Fatalf("the file must sync with the store it just wrote, got %+v", file)
	}
}
