package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const backupOperationLockName = ".backup-operation.lock"

type backupOperationContextKey struct{}

func withBackupOperationLock(ctx context.Context) context.Context {
	return context.WithValue(ctx, backupOperationContextKey{}, true)
}

func ownsBackupOperationLock(ctx context.Context) bool {
	owned, present := ctx.Value(backupOperationContextKey{}).(bool)
	return present && owned
}

// lockBackupOperation serializes restore readers and retention until the caller
// closes the file. The kernel releases the lock if the process exits.
func lockBackupOperation(ctx context.Context, root string) (*os.File, error) {
	if root == "" {
		return nil, errors.New("backup operation requires TACK_BACKUP_ROOT")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		slog.ErrorContext(ctx, "backup.lock.root_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("create backup root: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(root, backupOperationLockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		slog.ErrorContext(ctx, "backup.lock.open_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("open backup operation lock: %w", err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			slog.ErrorContext(ctx, "backup.lock.wait_failed", slog.String("err", err.Error()))
			_ = file.Close()
			return nil, fmt.Errorf("wait for backup operation lock: %w", err)
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			slog.ErrorContext(ctx, "backup.lock.failed", slog.String("err", err.Error()))
			_ = file.Close()
			return nil, fmt.Errorf("lock backup operation: %w", err)
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}
