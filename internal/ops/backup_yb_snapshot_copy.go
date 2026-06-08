package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// dumpYBSchema writes a schema-only ysql_dump of the database to outPath,
// running ysql_dump inside the live yugabyte container and streaming stdout to
// disk. The export_snapshot metadata references table ids this schema recreates
// on import; --include-yb-metadata preserves the YugabyteDB table properties
// the import path needs.
func dumpYBSchema(ctx context.Context, cli *client.Client, cfg *config.Config, outPath string) error {
	logger := telemetry.L(ctx)
	out, err := os.Create(outPath)
	if err != nil {
		wrapped := fmt.Errorf("create %s: %w", outPath, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.schema_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer out.Close()

	exitCode, stderr, err := containerExecStreaming(ctx, cli, yugabyteBackupContainer, []string{
		"/home/yugabyte/postgres/bin/ysql_dump",
		"-h", "yugabyte",
		"-p", "5433",
		"-U", cfg.YugabyteUser,
		"-d", cfg.YugabyteDB,
		"--schema-only",
		"--include-yb-metadata",
		"--no-owner",
		"--no-privileges",
	}, []string{"PGPASSWORD=" + cfg.YugabytePassword}, out)
	if err != nil {
		wrapped := fmt.Errorf("ysql_dump schema exec: %w", err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.schema_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if exitCode != 0 {
		wrapped := fmt.Errorf("ysql_dump schema exited %d: %s", exitCode, stderr)
		logger.ErrorContext(ctx, "backup.yb_snapshot.schema_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	info, err := os.Stat(outPath)
	if err != nil {
		wrapped := fmt.Errorf("stat %s: %w", outPath, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.schema_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if info.Size() == 0 {
		wrapped := fmt.Errorf("ysql_dump schema produced 0 bytes; refuse to ship empty schema")
		logger.ErrorContext(ctx, "backup.yb_snapshot.schema_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.yb_snapshot.schema_dumped",
		slog.String("path", outPath), slog.Int64("bytes", info.Size()))
	return nil
}

// tarYBTabletSnapshots streams a gzip tar of every tablet's snapshot directory
// for snapshotID, running find and tar inside the live yugabyte container. The
// script exits 3 when no snapshot directories match, so an empty tar (a few
// non-zero bytes, which would otherwise pass) cannot report a false success.
// -print0 with --null avoids path-splitting and ARG_MAX limits.
func tarYBTabletSnapshots(ctx context.Context, cli *client.Client, cfg *config.Config, snapshotID, outPath string) error {
	logger := telemetry.L(ctx)
	out, err := os.Create(outPath)
	if err != nil {
		wrapped := fmt.Errorf("create %s: %w", outPath, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.tablets_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer out.Close()

	script := fmt.Sprintf(
		"set -euo pipefail; cd %q; "+
			"if [ \"$(find . -type d -path '*.snapshots/%s' | wc -l)\" -eq 0 ]; then "+
			"echo 'no tablet snapshot dirs for %s' >&2; exit 3; fi; "+
			"find . -type d -path '*.snapshots/%s' -print0 | tar czf - --null --files-from -",
		cfg.BackupYBRocksDBDir, snapshotID, snapshotID, snapshotID,
	)
	exitCode, stderr, err := containerExecStreaming(ctx, cli, yugabyteBackupContainer,
		[]string{"bash", "-c", script}, nil, out)
	if err != nil {
		wrapped := fmt.Errorf("tablet snapshot tar exec: %w", err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.tablets_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if exitCode != 0 {
		wrapped := fmt.Errorf("tablet snapshot tar exited %d: %s", exitCode, stderr)
		logger.ErrorContext(ctx, "backup.yb_snapshot.tablets_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	info, err := os.Stat(outPath)
	if err != nil {
		wrapped := fmt.Errorf("stat %s: %w", outPath, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.tablets_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if info.Size() == 0 {
		wrapped := fmt.Errorf("tablet snapshot tar produced 0 bytes for snapshot %s", snapshotID)
		logger.ErrorContext(ctx, "backup.yb_snapshot.tablets_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.yb_snapshot.tablets_tarred",
		slog.String("snapshot_id", snapshotID), slog.Int64("bytes", info.Size()))
	return nil
}

// writeYBSnapshotManifest records the snapshot id, database, and run id so the
// restore path can find the snapshot the tablet files and metadata belong to.
func writeYBSnapshotManifest(ctx context.Context, path, runID, snapshotID, database string) error {
	logger := telemetry.L(ctx)
	manifest := map[string]string{
		"run_id":      runID,
		"snapshot_id": snapshotID,
		"database":    database,
		"created_at":  opsNow().UTC().Format(time.RFC3339),
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		wrapped := fmt.Errorf("marshal yb snapshot manifest: %w", err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.manifest_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		wrapped := fmt.Errorf("write yb snapshot manifest %s: %w", path, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.manifest_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

// ybSnapshotKeyPrefix is the object-store key prefix for one snapshot-export
// run. All four artifacts live under it.
func ybSnapshotKeyPrefix(runID string) string {
	return "yugabyte-snapshot/" + runID + "/"
}

// uploadYBSnapshotArtifacts uploads each staged file to the main backup bucket
// under the run's key prefix. files maps the object base name to its local
// path.
func uploadYBSnapshotArtifacts(ctx context.Context, cfg *config.Config, runID string, files map[string]string) error {
	logger := telemetry.L(ctx)
	s3Client := newBackupS3Client(cfg)
	prefix := ybSnapshotKeyPrefix(runID)
	for name, path := range files {
		if err := putObjectFromFile(ctx, s3Client, cfg.BackupS3BucketMain, prefix+name, path); err != nil {
			return err
		}
	}
	logger.InfoContext(
		ctx, "backup.yb_snapshot.uploaded",
		slog.String("bucket", cfg.BackupS3BucketMain),
		slog.String("prefix", prefix),
		slog.Int("files", len(files)),
	)
	return nil
}
