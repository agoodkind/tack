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

	res, err := containerExec(ctx, b.Cli, temporalDBBackupContainer, []string{
		"pg_dump",
		"-h", "localhost",
		"-U", b.Cfg.BackupTemporalDBUser,
		"-d", b.Cfg.BackupTemporalDBName,
		"--format=plain",
		"--no-owner",
		"--no-privileges",
	}, []string{"PGPASSWORD=" + b.Cfg.BackupTemporalDBPass})
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.temporal_db.exec_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("pg_dump exec: %w", err)
	}
	if res.ExitCode != 0 {
		dumpErr := fmt.Errorf("pg_dump exited %d: %s", res.ExitCode, res.Stderr)
		b.Log.ErrorContext(ctx, "backup.temporal_db.exit_nonzero",
			slog.Int("exit", res.ExitCode),
			slog.String("stderr", res.Stderr),
			slog.Any("err", dumpErr),
		)
		return dumpErr
	}
	_, err = out.WriteString(res.Stdout)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.temporal_db.write_failed",
			slog.String("path", outPath),
			slog.Any("err", err),
		)
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	if len(res.Stdout) == 0 {
		emptyErr := fmt.Errorf("pg_dump produced 0 bytes; refuse to ship empty dump")
		b.Log.ErrorContext(ctx, "backup.temporal_db.empty_dump", "err", emptyErr)
		return emptyErr
	}
	b.Log.InfoContext(ctx, "backup.temporal_db.complete",
		slog.String("artifact", outPath),
		slog.Int("bytes", len(res.Stdout)),
	)
	return nil
}
