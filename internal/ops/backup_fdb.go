package ops

import (
	"context"
	"fmt"
	"log/slog"
)

// runBackupFDB ensures FoundationDB continuously streams to the object store.
// The persistent fdb-backup-agent Compose service drains the backup.
func runBackupFDB(ctx context.Context, b *backupCtx) error {
	if b.Cfg.BackupS3Endpoint == "" {
		err := fmt.Errorf("TACK_BACKUP_S3_ENDPOINT is required for continuous FoundationDB backup")
		b.Log.ErrorContext(ctx, "backup.fdb.object_store_required", slog.Any("err", err))
		return err
	}
	return ensureFDBContinuousSession(ctx, b)
}
