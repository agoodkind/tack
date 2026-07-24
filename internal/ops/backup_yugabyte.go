package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// yugabyteBackupContainer is the named live Yugabyte container the
// production compose stack ships. The shell scripts assume the same name.
const yugabyteBackupContainer = "tack-yugabyte-1"

// runBackupYugabyte writes a plain-SQL ysql_dump of the tack database to
// `<dest>/yugabyte.sql`. Two of the four 2026-05-09 fixes live here:
//   - schema=public + schema=audit so the audit ledger is preserved
//   - ysql_dump (not pg_dump); the YugabyteDB image ships a fork of
//     pg_dump under /home/yugabyte/postgres/bin/ysql_dump and standard
//     pg_dump is absent from the image
func runBackupYugabyte(ctx context.Context, b *backupCtx) error {
	outPath := filepath.Join(b.DestDir, "yugabyte.sql")
	out, err := os.Create(outPath)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.yugabyte.create_failed",
			slog.String("path", outPath),
			slog.Any("err", err),
		)
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer out.Close()

	exitCode, stderr, err := containerExecStreaming(ctx, b.Cli, yugabyteBackupContainer, []string{
		"/home/yugabyte/postgres/bin/ysql_dump",
		"-h", "yugabyte",
		"-p", "5433",
		"-U", b.Cfg.YugabyteUser,
		"-d", b.Cfg.YugabyteDB,
		"--schema=public",
		"--schema=audit",
		"--format=plain",
		"--no-owner",
		"--no-privileges",
	}, []string{"PGPASSWORD=" + b.Cfg.YugabytePassword}, out)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.yugabyte.exec_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("ysql_dump exec: %w", err)
	}
	if exitCode != 0 {
		dumpErr := fmt.Errorf("ysql_dump exited %d: %s", exitCode, stderr)
		b.Log.ErrorContext(ctx, "backup.yugabyte.exit_nonzero",
			slog.Int("exit", exitCode),
			slog.String("stderr", stderr),
			slog.Any("err", dumpErr),
		)
		return dumpErr
	}
	// Re-stat to learn the streamed file's size and verify non-empty. The
	// streaming write went straight to disk; we never held the dump in Go
	// memory (TACK-264 OOM fix: previously buffered ~1.07 GB into a Go string).
	info, err := os.Stat(outPath)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.yugabyte.stat_failed",
			slog.String("path", outPath),
			slog.Any("err", err),
		)
		return fmt.Errorf("stat %s: %w", outPath, err)
	}
	if info.Size() == 0 {
		emptyErr := fmt.Errorf("ysql_dump produced 0 bytes; refuse to ship empty dump")
		b.Log.ErrorContext(ctx, "backup.yugabyte.empty_dump", "err", emptyErr)
		return emptyErr
	}
	b.Log.InfoContext(ctx, "backup.yugabyte.complete",
		slog.String("artifact", outPath),
		slog.Int64("bytes", info.Size()),
	)
	return nil
}
