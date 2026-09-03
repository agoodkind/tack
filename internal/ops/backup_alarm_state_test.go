package ops

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBackupStalenessAlarmRefusesASymlinkedStateTemporary plants a symbolic
// link at the fixed temporary name the state write uses, pointing at a file
// outside the backup root, and runs a stale check whose mail is accepted. The
// write must fail at the link rather than follow it: the target keeps its
// bytes and no state is recorded, so the next run mails again rather than
// having truncated a file of the planter's choosing.
func TestBackupStalenessAlarmRefusesASymlinkedStateTemporary(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captured := captureBackupAlarmSends(t, nil)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")

	target := filepath.Join(t.TempDir(), "outside-the-backup-root")
	if err := os.WriteFile(target, []byte("bytes the checker must not touch"), 0o600); err != nil {
		t.Fatalf("place the target: %v", err)
	}
	partial := backupAlarmStatePath(cfg) + ".partial"
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

// TestBackupStalenessAlarmReplacesAStaleStateTemporary leaves a regular file
// at the temporary name, the residue of a run that crashed between writing and
// renaming, and proves the next accepted mail is recorded over it.
func TestBackupStalenessAlarmReplacesAStaleStateTemporary(t *testing.T) {
	fixBackupStalenessClock(t, time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	captureBackupAlarmSends(t, nil)
	cfg := unreachableBackupStalenessConfig(t, "backups@example.test")
	partial := backupAlarmStatePath(cfg) + ".partial"
	if err := os.WriteFile(partial, []byte("{\"alarmed\":{\"half-written"), 0o600); err != nil {
		t.Fatalf("leave the stale temporary: %v", err)
	}

	runStaleBackupStalenessCheck(t, cfg)
	alarmed, found := alarmedBackupMetrics(t, cfg)
	if !found || len(alarmed) != 3 {
		t.Fatalf("the accepted mail must be recorded over the stale temporary, found = %v state = %v", found, alarmed)
	}
	if _, err := os.Lstat(partial); !os.IsNotExist(err) {
		t.Fatalf("the temporary must have been renamed into place, lstat err = %v", err)
	}
}
