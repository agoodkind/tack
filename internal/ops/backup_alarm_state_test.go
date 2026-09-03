package ops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// pinBackupAlarmPartialSuffix makes every temporary this test stages carry
// suffix, so an entry can be planted at the name before the write.
func pinBackupAlarmPartialSuffix(t *testing.T, suffix string) {
	t.Helper()
	previous := backupAlarmPartialSuffix
	backupAlarmPartialSuffix = func(context.Context) (string, error) { return suffix, nil }
	t.Cleanup(func() { backupAlarmPartialSuffix = previous })
}

// TestBackupStalenessAlarmRefusesASymlinkedStateTemporary plants a symbolic
// link at the temporary name the state write will use, pointing at a file
// outside the backup root, and runs a stale check whose mail is accepted. The
// write must fail at the link rather than follow it: the target keeps its
// bytes and no state is recorded, so the next run mails again rather than
// having truncated a file of the planter's choosing.
func TestBackupStalenessAlarmRefusesASymlinkedStateTemporary(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	pinBackupAlarmPartialSuffix(t, "planted")
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")

	target := filepath.Join(t.TempDir(), "outside-the-backup-root")
	if err := os.WriteFile(target, []byte("bytes the checker must not touch"), 0o600); err != nil {
		t.Fatalf("place the target: %v", err)
	}
	partial := backupAlarmStatePath(cfg) + ".partial.planted"
	if err := os.Symlink(target, partial); err != nil {
		t.Fatalf("plant the symlink: %v", err)
	}

	runStaleBackupStalenessCheck(t, cfg)
	if len(captured.messages) != 1 {
		t.Fatalf("the fault must still mail once, sent %d", len(captured.messages))
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read the target: %v", err)
	}
	if string(body) != "bytes the checker must not touch" {
		t.Fatalf("the write followed the link: target now holds %q", body)
	}
	if _, found := alarmedBackupMetrics(t, cfg); found {
		t.Fatal("a refused write must leave no state file")
	}
	if info, err := os.Lstat(partial); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the planted link must be left where it was, lstat = %v, %v", info, err)
	}
}

// TestBackupStalenessAlarmLeavesAnotherRunsTemporary leaves a temporary with a
// suffix this run did not draw, the residue of a crashed run or the live file
// of an overlapping one, and proves the accepted mail is recorded beside it
// while the file is neither removed nor renamed: no invocation can tell those
// two origins apart, so none touches a temporary it did not create.
func TestBackupStalenessAlarmLeavesAnotherRunsTemporary(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captureBackupAlarmSends(t, nil)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	other := backupAlarmStatePath(cfg) + ".partial.0123456789abcdef"
	if err := os.WriteFile(other, []byte("{\"alarmed\":{\"half-written"), 0o600); err != nil {
		t.Fatalf("leave the other temporary: %v", err)
	}

	runStaleBackupStalenessCheck(t, cfg)
	alarmed, found := alarmedBackupMetrics(t, cfg)
	if !found || len(alarmed) != 3 {
		t.Fatalf("the accepted mail must be recorded beside the other temporary, found = %v state = %v", found, alarmed)
	}
	left, err := os.ReadFile(other)
	if err != nil || string(left) != "{\"alarmed\":{\"half-written" {
		t.Fatalf("the other run's temporary must be left untouched, got %q, %v", left, err)
	}
}

// TestBackupStalenessAlarmInterleavedSavesLoseNothing stages two saves so both
// temporaries exist at once, then commits them in the opposite order, the
// shape of two overlapping runs. Each save must rename exactly its own bytes:
// both renames succeed, the state file ends as the save that committed last,
// and nothing is left behind, because neither save unlinked or moved the
// other's temporary.
func TestBackupStalenessAlarmInterleavedSavesLoseNothing(t *testing.T) {
	ctx := context.Background()
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	path := backupAlarmStatePath(cfg)
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	first := backupAlarmState{Alarmed: map[string]time.Time{backupStalenessRehearsalName: at}}
	second := backupAlarmState{Alarmed: map[string]time.Time{backupStalenessExportName: at}}
	firstBody, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	secondBody, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}

	firstPartial, err := stageBackupAlarmState(ctx, path, firstBody)
	if err != nil {
		t.Fatalf("stage first: %v", err)
	}
	secondPartial, err := stageBackupAlarmState(ctx, path, secondBody)
	if err != nil {
		t.Fatalf("stage second while the first is staged: %v", err)
	}
	if firstPartial == secondPartial {
		t.Fatalf("two staged saves share the temporary %s", firstPartial)
	}
	if err := os.Rename(secondPartial, path); err != nil {
		t.Fatalf("commit second: %v", err)
	}
	if err := os.Rename(firstPartial, path); err != nil {
		t.Fatalf("commit first after second: %v", err)
	}

	alarmed, found := alarmedBackupMetrics(t, cfg)
	if !found || len(alarmed) != 1 || !alarmed[backupStalenessRehearsalName].Equal(at) {
		t.Fatalf("the state must be the save committed last, found = %v state = %v", found, alarmed)
	}
	entries, err := os.ReadDir(cfg.BackupRoot)
	if err != nil {
		t.Fatalf("read the backup root: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != backupAlarmStateFile {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("only the state file may remain, found %v", names)
	}
}
