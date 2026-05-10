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
//
// Mirrors `tack_backup_dump_yugabyte` in backup-functions.sh:130-145.
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

	res, err := containerExec(ctx, b.Cli, yugabyteBackupContainer, []string{
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
	}, []string{"PGPASSWORD=" + b.Cfg.YugabytePassword})
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.yugabyte.exec_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("ysql_dump exec: %w", err)
	}
	if res.ExitCode != 0 {
		dumpErr := fmt.Errorf("ysql_dump exited %d: %s", res.ExitCode, res.Stderr)
		b.Log.ErrorContext(ctx, "backup.yugabyte.exit_nonzero",
			slog.Int("exit", res.ExitCode),
			slog.String("stderr", res.Stderr),
			slog.Any("err", dumpErr),
		)
		return dumpErr
	}
	_, err = out.WriteString(res.Stdout)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.yugabyte.write_failed",
			slog.String("path", outPath),
			slog.Any("err", err),
		)
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	if len(res.Stdout) == 0 {
		emptyErr := fmt.Errorf("ysql_dump produced 0 bytes; refuse to ship empty dump")
		b.Log.ErrorContext(ctx, "backup.yugabyte.empty_dump", "err", emptyErr)
		return emptyErr
	}
	b.Log.InfoContext(ctx, "backup.yugabyte.complete",
		slog.String("artifact", outPath),
		slog.Int("bytes", len(res.Stdout)),
	)
	return nil
}
