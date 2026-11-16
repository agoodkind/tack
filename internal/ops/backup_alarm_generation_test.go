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

// readBackupAlarmStateFile reads local state, including its generation, or
// returns found=false when the file does not exist.
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

// readSharedBackupAlarmMemory reads shared state, including its generation,
// or returns found=false when the object does not exist.
func readSharedBackupAlarmMemory(t *testing.T, store *backupTestStore) (
	state backupAlarmState,
	found bool,
) {
	t.Helper()
	body, ok := store.object(backupAlarmMemoryKey)
	if !ok {
		return state, false
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode shared alarm memory %q: %v", body, err)
	}
	return state, true
}

// rehearsalFaultObjects provides fresh export and replication records without
// a successful rehearsal marker.
func rehearsalFaultObjects(t *testing.T, now time.Time) map[string][]byte {
	t.Helper()
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
	return objects
}

// TestBackupStalenessAlarmAdoptsAClearTheOtherCheckerRecorded checks that a
// newer shared generation replaces a stale local fault before another failure.
// Union alone would incorrectly suppress the second fault's email.
func TestBackupStalenessAlarmAdoptsAClearTheOtherCheckerRecorded(t *testing.T) {
	now := time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC)
	fixBackupStalenessClock(t, now)
	captured := captureBackupAlarmSends(t, nil)
	store := newBackupTestStore(t, rehearsalFaultObjects(t, now))
	owner := storedBackupStalenessConfig(t, store)
	deputy := storedDeputyBackupStalenessConfig(t, store)
	seesHealthyCluster(t, owner, deputy)
	rehearsalKey := backupStatusKey(backupStalenessRehearsalName)

	runStaleBackupStalenessCheck(t, owner)
	if len(captured.messages) != 1 {
		t.Fatalf("the first fault must email once, sent %d", len(captured.messages))
	}
	ownerFile, _ := readBackupAlarmStateFile(t, owner)
	if ownerFile.Generation != 1 || len(ownerFile.Alarmed) != 1 {
		t.Fatalf("local owner state must match shared generation 1, got %+v", ownerFile)
	}

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
	shared, _ := readSharedBackupAlarmMemory(t, store)
	if shared.Generation != 2 || len(shared.Alarmed) != 0 {
		t.Fatalf(
			"deputy recovery must produce shared generation 2 with an empty fault map, got %+v",
			shared,
		)
	}

	store.remove(rehearsalKey)
	logs := runStaleDeputyCheck(t, owner)
	if len(captured.messages) != 2 {
		t.Fatalf(
			"the owner must adopt shared recovery and email the second fault, sent %d total",
			len(captured.messages),
		)
	}
	if !strings.Contains(logs, "msg=backup.staleness.alarm_memory_adopted") ||
		!strings.Contains(logs, "generation=2 local_generation=1") {
		t.Fatalf("the owner must log adoption of newer shared state:\n%s", logs)
	}
	shared, _ = readSharedBackupAlarmMemory(t, store)
	ownerFile, _ = readBackupAlarmStateFile(t, owner)
	if shared.Generation != 3 || ownerFile.Generation != 3 || len(shared.Alarmed) != 1 {
		t.Fatalf(
			"the second email must update shared and local state to generation 3, shared %+v local %+v",
			shared,
			ownerFile,
		)
	}
}

// TestBackupAlarmCacheSuppressesDuplicatesDuringSharedOutage checks local
// caching of shared fault state before a subsequent shared read failure.
func TestBackupAlarmCacheSuppressesDuplicatesDuringSharedOutage(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	store := newBackupTestStore(t, nil)
	owner := storedBackupStalenessConfig(t, store)
	deputy := storedDeputyBackupStalenessConfig(t, store)

	runStaleBackupStalenessCheck(t, owner)
	runStaleDeputyCheck(t, deputy)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"shared state must suppress the deputy's duplicate email, sent %d total",
			len(captured.messages),
		)
	}
	cached, found := readBackupAlarmStateFile(t, deputy)
	if !found || len(cached.Alarmed) != 3 || cached.Generation != 1 {
		t.Fatalf(
			"the deputy must cache shared faults at the shared generation, found = %v state = %+v",
			found,
			cached,
		)
	}

	store.put(backupAlarmMemoryKey, bytes.Repeat([]byte("x"), smallObjectMaxBytes+1))
	logs := runStaleDeputyCheck(t, deputy)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"cached faults must suppress email during a shared read failure, sent %d total",
			len(captured.messages),
		)
	}
	if !strings.Contains(logs, "msg=backup.staleness.alarm_memory_unreadable") ||
		!strings.Contains(logs, "msg=backup.staleness.alarm_held") {
		t.Fatalf("logs must report unreadable shared state and alarm suppression:\n%s", logs)
	}
}

// TestBackupStalenessAlarmWritesTheStoreOnTheRunAfterAFailedPut checks local
// recording at the old shared generation after a refused write. Restoring writer
// access must merge and upload that record without repeating the email.
func TestBackupStalenessAlarmWritesTheStoreOnTheRunAfterAFailedPut(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 9, 6, 1, 25, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	store := newBackupTestStore(t, nil)
	cfg := store.config()
	cfg.BackupRoot = t.TempDir()
	cfg.BackupYBMasterAddresses = "127.0.0.1:7100"
	cfg.BackupStalenessExportMaxSeconds = 129600
	cfg.BackupStalenessRehearsalMaxSeconds = 691200
	cfg.BackupStalenessReplicationMaxSeconds = 1800
	cfg.BackupAlarmEmail = "backups@example.test"

	store.signAsReader(cfg)
	logs := runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"refused state writes must not prevent the first fault email, sent %d",
			len(captured.messages),
		)
	}
	if !strings.Contains(logs, "level=ERROR msg=backup.staleness.alarm_memory_write_failed") ||
		!strings.Contains(logs, "AccessDenied") {
		t.Fatalf("the error log must include the refused write and its cause:\n%s", logs)
	}
	if _, found := readSharedBackupAlarmMemory(t, store); found {
		t.Fatal("a refused write must not create a shared state object")
	}
	file, found := readBackupAlarmStateFile(t, cfg)
	if !found || len(file.Alarmed) != 3 || file.Generation != 0 {
		t.Fatalf(
			"local state must record email at the previous shared generation, found = %v state = %+v",
			found,
			file,
		)
	}

	store.signAsWriter(cfg)
	runStaleDeputyCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf(
			"local state must suppress duplicate email on retry, sent %d total",
			len(captured.messages),
		)
	}
	shared, found := readSharedBackupAlarmMemory(t, store)
	if !found || len(shared.Alarmed) != 3 || shared.Generation != 1 {
		t.Fatalf(
			"retry must upload merged state at generation 1, found = %v state = %+v",
			found,
			shared,
		)
	}
	if file, _ := readBackupAlarmStateFile(t, cfg); file.Generation != 1 {
		t.Fatalf("local state must match the successful shared write, got %+v", file)
	}
}
