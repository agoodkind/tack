// backup_yb_snapshot.go exports a YugabyteDB distributed snapshot off-host to
// the object store, the last-resort backup tier for the SQL store. Disaster
// recovery for YugabyteDB is quorum replication plus a standby cluster; the
// in-cluster snapshot schedule is the corruption layer and does not survive
// loss of the host's storage. This export does. See
// https://docs.yugabyte.com/stable/manage/backup-restore/snapshot-ysql/

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// ybSnapshotCompleteTimeout bounds the wait for create_database_snapshot to
	// reach COMPLETE. A single-node database snapshot completes in seconds; the
	// budget is generous to tolerate a busy node.
	ybSnapshotCompleteTimeout = 5 * time.Minute
	// ybSnapshotPollInterval is the gap between list_snapshots polls.
	ybSnapshotPollInterval = 3 * time.Second
)

// ybSnapshotStatus is the lifecycle state yb-admin reports for a snapshot.
type ybSnapshotStatus string

const (
	ybSnapStatusUnknown  ybSnapshotStatus = ""
	ybSnapStatusCreating ybSnapshotStatus = "CREATING"
	ybSnapStatusComplete ybSnapshotStatus = "COMPLETE"
	ybSnapStatusFailed   ybSnapshotStatus = "FAILED"
)

// RunBackupYBSnapshotExport takes a YugabyteDB distributed snapshot and exports
// it off-host: the snapshot metadata, a schema-only ysql_dump, and the tablet
// snapshot files, all under one run key in the object store. yb-admin runs in
// one-shot containers (the same path as the snapshot-schedule command); the
// schema dump and the tablet tar run inside the live yugabyte container.
func RunBackupYBSnapshotExport(ctx context.Context, cfg *config.Config) error {
	logger := telemetry.L(ctx)
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := fmt.Errorf("yb-snapshot-export: TACK_BACKUP_S3_ENDPOINT, _ACCESS_KEY_ID, and _SECRET_ACCESS_KEY are required")
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", err.Error()))
		return err
	}
	if cfg.YugabytePassword == "" {
		err := fmt.Errorf("yb-snapshot-export: YUGABYTE_PASSWORD is required")
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", err.Error()))
		return err
	}

	filter := "ysql." + cfg.YugabyteDB
	runID := opsNow().UTC().Format("20060102T150405Z")
	stageDir := filepath.Join(cfg.BackupRoot, "yb-snapshot-"+runID)
	if err := os.MkdirAll(stageDir, 0o750); err != nil {
		wrapped := fmt.Errorf("mkdir yb snapshot stage %s: %w", stageDir, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	// export_snapshot runs in a one-shot and writes the metadata file into this
	// dir via a bind mount, so it must be writable by the container user. The
	// dir is transient and removed after the upload succeeds.
	if err := os.Chmod(stageDir, 0o777); err != nil {
		wrapped := fmt.Errorf("chmod yb snapshot stage %s: %w", stageDir, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	cli, err := newDockerClient(ctx)
	if err != nil {
		return err
	}
	defer cli.Close()

	logger.InfoContext(
		ctx, "backup.yb_snapshot.start",
		slog.String("filter", filter),
		slog.String("run_id", runID),
		slog.String("masters", cfg.BackupYBMasterAddresses),
	)

	createRes, err := ybAdminOneShot(ctx, cli, cfg, nil, "create_database_snapshot", filter)
	if err != nil {
		return err
	}
	snapshotID := parseSnapshotID(createRes.Stdout)
	if snapshotID == "" {
		wrapped := fmt.Errorf("yb-snapshot-export: could not parse snapshot id from %q", strings.TrimSpace(createRes.Stdout))
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.yb_snapshot.created", slog.String("snapshot_id", snapshotID))

	if err := waitYBSnapshotComplete(ctx, cli, cfg, snapshotID); err != nil {
		return err
	}

	if err := exportYBSnapshotToObjectStore(ctx, cli, cfg, runID, stageDir, snapshotID); err != nil {
		return err
	}

	// The in-cluster snapshot is exported; delete it so one-off exports do not
	// accumulate. Best effort: a failed delete must not fail a good backup.
	if _, delErr := ybAdminOneShot(ctx, cli, cfg, nil, "delete_snapshot", snapshotID); delErr != nil {
		logger.WarnContext(ctx, "backup.yb_snapshot.delete_failed",
			slog.String("snapshot_id", snapshotID), slog.String("err", delErr.Error()))
	}

	if rmErr := os.RemoveAll(stageDir); rmErr != nil {
		logger.WarnContext(ctx, "backup.yb_snapshot.stage_cleanup_failed",
			slog.String("dir", stageDir), slog.String("err", rmErr.Error()))
	}

	logger.InfoContext(
		ctx, "backup.yb_snapshot.completed",
		slog.String("run_id", runID),
		slog.String("snapshot_id", snapshotID),
		slog.String("bucket", cfg.BackupS3BucketMain),
		slog.String("key_prefix", ybSnapshotKeyPrefix(runID)),
	)
	return nil
}

// exportYBSnapshotToObjectStore runs the post-creation pipeline for a snapshot
// already known COMPLETE: export the metadata file, dump the schema, tar the
// tablet snapshot files, write the manifest, and upload all four under the
// run's key prefix.
func exportYBSnapshotToObjectStore(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	runID, stageDir, snapshotID string,
) error {
	const metaName = "metadata.snapshot"
	if _, err := ybAdminOneShot(ctx, cli, cfg,
		[]string{stageDir + ":/out"},
		"export_snapshot", snapshotID, "/out/"+metaName); err != nil {
		return err
	}

	schemaPath := filepath.Join(stageDir, "schema.sql")
	if err := dumpYBSchema(ctx, cli, cfg, schemaPath); err != nil {
		return err
	}

	tabletsPath := filepath.Join(stageDir, "tablets.tar.gz")
	if err := tarYBTabletSnapshots(ctx, cli, cfg, snapshotID, tabletsPath); err != nil {
		return err
	}

	manifestPath := filepath.Join(stageDir, "manifest.json")
	if err := writeYBSnapshotManifest(ctx, manifestPath, runID, snapshotID, cfg.YugabyteDB); err != nil {
		return err
	}

	return uploadYBSnapshotArtifacts(ctx, cfg, runID, map[string]string{
		"manifest.json":  manifestPath,
		metaName:         filepath.Join(stageDir, metaName),
		"schema.sql":     schemaPath,
		"tablets.tar.gz": tabletsPath,
	})
}

// ybAdminOneShot runs one yb-admin subcommand in a one-shot container on the
// tack compose network, prepending the master_addresses flag. binds is optional
// (export_snapshot needs the staging dir bound to write its metadata file). It
// returns an error on a non-zero exit so callers do not inspect codes.
func ybAdminOneShot(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	binds []string,
	args ...string,
) (execResult, error) {
	logger := telemetry.L(ctx)
	cmd := append([]string{"--master_addresses", cfg.BackupYBMasterAddresses}, args...)
	res, err := runOneShot(ctx, cli, logger, runOneShotOptions{
		Image:      cfg.BackupYBPITRImage,
		Network:    cfg.BackupFDBNetwork,
		Entrypoint: []string{ybAdminBinary},
		Cmd:        cmd,
		Env:        nil,
		Binds:      binds,
		ExtraHosts: nil,
		Name:       "",
	})
	if err != nil {
		wrapped := fmt.Errorf("yb-admin %s: %w", args[0], err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.yb_admin_failed",
			slog.String("subcommand", args[0]), slog.String("err", wrapped.Error()))
		return res, wrapped
	}
	if res.ExitCode != 0 {
		wrapped := fmt.Errorf("yb-admin %s exited %d: %s", args[0], res.ExitCode,
			strings.TrimSpace(res.Stdout+" "+res.Stderr))
		logger.ErrorContext(ctx, "backup.yb_snapshot.yb_admin_failed",
			slog.String("subcommand", args[0]), slog.String("err", wrapped.Error()))
		return res, wrapped
	}
	return res, nil
}

// parseSnapshotID extracts the snapshot UUID from create_database_snapshot
// output, which prints "Started snapshot creation: <uuid>". An empty return
// means the marker was absent, which the caller treats as a failure.
func parseSnapshotID(stdout string) string {
	_, after, found := strings.Cut(stdout, "creation:")
	if !found {
		return ""
	}
	fields := strings.Fields(after)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// waitYBSnapshotComplete polls list_snapshots until the snapshot reaches
// COMPLETE, errors on FAILED, and times out via context so the poll loop never
// consults the wall clock directly.
func waitYBSnapshotComplete(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	snapshotID string,
) error {
	logger := telemetry.L(ctx)
	pollCtx, cancel := context.WithTimeout(ctx, ybSnapshotCompleteTimeout)
	defer cancel()
	ticker := time.NewTicker(ybSnapshotPollInterval)
	defer ticker.Stop()
	for {
		res, err := ybAdminOneShot(pollCtx, cli, cfg, nil, "list_snapshots")
		if err == nil {
			switch ybSnapshotState(res.Stdout, snapshotID) {
			case ybSnapStatusComplete:
				return nil
			case ybSnapStatusFailed:
				wrapped := fmt.Errorf("yb-snapshot-export: snapshot %s reported FAILED", snapshotID)
				logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))
				return wrapped
			case ybSnapStatusUnknown, ybSnapStatusCreating:
				// Not ready yet; fall through to the wait below.
			}
		}
		select {
		case <-pollCtx.Done():
			wrapped := fmt.Errorf("yb-snapshot-export: snapshot %s did not reach COMPLETE within %s: %w",
				snapshotID, ybSnapshotCompleteTimeout, pollCtx.Err())
			logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))
			return wrapped
		case <-ticker.C:
		}
	}
}

// ybSnapshotState returns the status on the list_snapshots line that names
// snapshotID, or ybSnapStatusUnknown if the snapshot is not listed yet.
func ybSnapshotState(listOutput, snapshotID string) ybSnapshotStatus {
	for line := range strings.SplitSeq(listOutput, "\n") {
		if !strings.Contains(line, snapshotID) {
			continue
		}
		switch {
		case strings.Contains(line, string(ybSnapStatusComplete)):
			return ybSnapStatusComplete
		case strings.Contains(line, string(ybSnapStatusFailed)):
			return ybSnapStatusFailed
		default:
			return ybSnapStatusCreating
		}
	}
	return ybSnapStatusUnknown
}
