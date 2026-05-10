package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// temporalDBBackupContainer is the named live Temporal-DB container the
// production compose stack ships. The shell scripts assume the same name.
const temporalDBBackupContainer = "tack-temporal-db-1"

// runBackupTemporalDB pg_dumps the Temporal-DB database. This is the fourth
// 2026-05-09 fix preserved: without it, in-flight workflow state has no
// recovery artifact. Mirrors `tack_backup_dump_temporal_db` in
// backup-functions.sh:149-161. The Temporal-DB image is plain Postgres so
// pg_dump (not ysql_dump) is the right binary here.
func runBackupTemporalDB(ctx context.Context, b *backupCtx) error {
	outPath := filepath.Join(b.DestDir, "temporal-db.sql")
	out, err := os.Create(outPath)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.temporal_db.create_failed",
			slog.String("path", outPath),
			slog.Any("err", err),
		)
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer out.Close()

	exitCode, stderr, err := containerExecStreaming(ctx, b.Cli, temporalDBBackupContainer, []string{
		"pg_dump",
		"-h", "localhost",
		"-U", b.Cfg.BackupTemporalDBUser,
		"-d", b.Cfg.BackupTemporalDBName,
		"--format=plain",
		"--no-owner",
		"--no-privileges",
	}, []string{"PGPASSWORD=" + b.Cfg.BackupTemporalDBPass}, out)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.temporal_db.exec_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("pg_dump exec: %w", err)
	}
	if exitCode != 0 {
		dumpErr := fmt.Errorf("pg_dump exited %d: %s", exitCode, stderr)
		b.Log.ErrorContext(ctx, "backup.temporal_db.exit_nonzero",
			slog.Int("exit", exitCode),
			slog.String("stderr", stderr),
			slog.Any("err", dumpErr),
		)
		return dumpErr
	}
	// See backup_yugabyte.go: streaming write to disk avoids buffering the
	// entire dump in Go memory.
	info, err := os.Stat(outPath)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.temporal_db.stat_failed",
			slog.String("path", outPath),
			slog.Any("err", err),
		)
		return fmt.Errorf("stat %s: %w", outPath, err)
	}
	if info.Size() == 0 {
		emptyErr := fmt.Errorf("pg_dump produced 0 bytes; refuse to ship empty dump")
		b.Log.ErrorContext(ctx, "backup.temporal_db.empty_dump", "err", emptyErr)
		return emptyErr
	}
	b.Log.InfoContext(ctx, "backup.temporal_db.complete",
		slog.String("artifact", outPath),
		slog.Int64("bytes", info.Size()),
	)
	return nil
}
