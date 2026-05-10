package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// runBackupMeilisearch tars the Meilisearch named volume. Meilisearch has
// no anonymous-volume shadowing in its image, so the prior volume-tar path
// remains correct here. We use a one-shot alpine container so the host's
// tar variant does not matter.
//
// Mirrors `tack_backup_dump_meilisearch` in backup-functions.sh:167-175.
func runBackupMeilisearch(ctx context.Context, b *backupCtx) error {
	outPath := filepath.Join(b.DestDir, "tack_meili-data.tar.gz")

	args := []string{
		"run", "--rm",
		"-v", b.Cfg.BackupMeiliVolume + ":/src:ro",
		"-v", b.DestDir + ":/dst",
		"alpine",
		"sh", "-c",
		"cd /src && tar czf /dst/" + filepath.Base(outPath) + " .",
	}
	_, err := runDockerCmd(ctx, b.Log, args...)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.meilisearch.tar_failed",
			slog.String("output", outPath),
			slog.Any("err", err),
		)
		return fmt.Errorf("meilisearch tar: %w", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.meilisearch.stat_failed",
			slog.String("path", outPath),
			slog.Any("err", err),
		)
		return fmt.Errorf("stat meili artifact: %w", err)
	}
	if info.Size() == 0 {
		emptyErr := fmt.Errorf("meilisearch tar wrote 0 bytes; refuse to ship empty artifact")
		b.Log.ErrorContext(ctx, "backup.meilisearch.empty", "err", emptyErr)
		return emptyErr
	}
	b.Log.InfoContext(ctx, "backup.meilisearch.complete",
		slog.String("artifact", outPath),
		slog.Int64("bytes", info.Size()),
	)
	return nil
}
