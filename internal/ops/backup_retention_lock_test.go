package ops_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"goodkind.io/tack/internal/ops"
)

func TestBackupRetentionAndRestoreSerializeUntilCancellation(t *testing.T) {
	fixture := newRetentionCommandFixture(t)
	fixture.completeRun(t, fixture.cutoff)
	fixture.store.stop()
	defer fixture.store.ensureStarted()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- ops.PreviewBackupRetention(ctx, fixture.config, ops.BackupRetentionOptions{KeepDays: 14}, io.Discard)
	}()
	waitForRetentionLock(t, fixture.config.BackupRoot)
	restoreContext, cancelRestore := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancelRestore()
	err := ops.RunBackupRestoreDrill(restoreContext, fixture.config, ops.RestoreDrillOptions{})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "backup operation lock") {
		t.Fatalf("concurrent restore did not wait for retention: %v", err)
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled retention returned %v", err)
	}
	fixture.config.BackupS3Endpoint = fixture.store.start()
	if err := ops.PreviewBackupRetention(t.Context(), fixture.config, ops.BackupRetentionOptions{KeepDays: 14}, io.Discard); err != nil {
		t.Fatalf("retention lock did not release after cancellation: %v", err)
	}
}

func waitForRetentionLock(t *testing.T, root string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		file, err := os.OpenFile(filepath.Join(root, backupOperationLockName), os.O_RDWR, 0)
		if err == nil {
			err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			_ = file.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return
			}
		}
		select {
		case <-deadline.C:
			t.Fatal("retention did not acquire the shared backup lock")
		case <-ticker.C:
		}
	}
}
