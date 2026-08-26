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

// RunBackupYBSnapshotExport orchestrates a YugabyteDB distributed-snapshot
// export: it creates the cluster snapshot, waits COMPLETE, and uploads the
// snapshot metadata, a schema-only ysql_dump, and a completeness manifest under
// one run key. Every helper runs in a one-shot container against the master
// quorum names; no local yugabyte container is required on the guest that runs
// this command. The tablet snapshot files stay on the data guests: each guest's
// `ops backup yb-archive-node` timer uploads its own node's tar under the
// prefix the manifest assigns it, and the restore drill refuses the run until
// every listed prefix is filled.
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

	// A prior run's in-cluster snapshot is only deletable once every node has
	// archived its tablet files, so cleanup happens here rather than at the end
	// of the run that created it. Best effort: a failed cleanup must not block
	// a new export.
	cleanupYBPriorExportSnapshot(ctx, cli, cfg)

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

	// The in-cluster snapshot must survive this command: the per-node archive
	// timers still read its tablet files from each data guest. The next export
	// run deletes it once the manifest is satisfied.

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
// already known COMPLETE: export the metadata file, dump the schema, write the
// completeness manifest from the live tablet-server list, and upload all three
// under the run's key prefix. The tablet archives themselves are each data
// guest's job (`ops backup yb-archive-node`); this pipeline only declares the
// node prefixes those archives must fill.
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
	if err := dumpYBSchemaOneShot(ctx, cli, cfg, stageDir, schemaPath); err != nil {
		return err
	}

	nodes, err := listYBTabletServerNodes(ctx, cli, cfg)
	if err != nil {
		return err
	}

	manifest := newYBSnapshotManifest(runID, snapshotID, cfg.YugabyteDB, nodes)
	manifestPath := filepath.Join(stageDir, ybSnapshotManifestObject)
	if err := writeYBSnapshotManifest(ctx, manifestPath, manifest); err != nil {
		return err
	}

	return uploadYBSnapshotArtifacts(ctx, cfg, runID, map[string]string{
		ybSnapshotManifestObject: manifestPath,
		metaName:                 filepath.Join(stageDir, metaName),
		"schema.sql":             schemaPath,
	})
}

// listYBTabletServerNodes derives the tablet-server node names live from
// yb-admin so the manifest tracks the cluster rather than a config value. An
// empty list is an error: a manifest that lists no nodes would gate nothing.
func listYBTabletServerNodes(ctx context.Context, cli *client.Client, cfg *config.Config) ([]string, error) {
	logger := telemetry.L(ctx)
	res, err := ybAdminOneShot(ctx, cli, cfg, nil, "list_all_tablet_servers")
	if err != nil {
		return nil, err
	}
	nodes := parseYBTabletServers(res.Stdout)
	if len(nodes) == 0 {
		wrapped := fmt.Errorf("yb-snapshot-export: list_all_tablet_servers reported no tablet servers: %q",
			strings.TrimSpace(res.Stdout))
		logger.ErrorContext(ctx, "backup.yb_snapshot.failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	logger.InfoContext(ctx, "backup.yb_snapshot.tablet_servers", slog.Any("nodes", nodes))
	return nodes, nil
}

// dumpYBSchemaOneShot writes a schema-only ysql_dump of the database into the
// bind-mounted stage dir, running ysql_dump in a one-shot container the same
// way yb-admin one-shots run and pointing it at the first master's node name.
// The export_snapshot metadata references table ids this schema recreates on
// import; --include-yb-metadata preserves the YugabyteDB table properties the
// import path needs.
func dumpYBSchemaOneShot(ctx context.Context, cli *client.Client, cfg *config.Config, stageDir, schemaPath string) error {
	logger := telemetry.L(ctx)
	host := ybFirstMasterHost(cfg.BackupYBMasterAddresses)
	res, err := runOneShot(ctx, cli, logger, runOneShotOptions{
		Image:      cfg.BackupYBPITRImage,
		Network:    cfg.BackupFDBNetwork,
		Entrypoint: []string{"/home/yugabyte/postgres/bin/ysql_dump"},
		Cmd: []string{
			"-h", host,
			"-p", "5433",
			"-U", cfg.YugabyteUser,
			"-d", cfg.YugabyteDB,
			"--schema-only",
			"--include-yb-metadata",
			"--no-owner",
			"--no-privileges",
			"-f", "/out/schema.sql",
		},
		Env:        []string{"PGPASSWORD=" + cfg.YugabytePassword},
		Binds:      []string{stageDir + ":/out"},
		ExtraHosts: nil,
		Name:       "",
	})
	if err != nil {
		wrapped := fmt.Errorf("ysql_dump schema one-shot: %w", err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.schema_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if res.ExitCode != 0 {
		wrapped := fmt.Errorf("ysql_dump schema exited %d: %s", res.ExitCode,
			strings.TrimSpace(res.Stdout+" "+res.Stderr))
		logger.ErrorContext(ctx, "backup.yb_snapshot.schema_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	info, err := os.Stat(schemaPath)
	if err != nil {
		wrapped := fmt.Errorf("stat %s: %w", schemaPath, err)
		logger.ErrorContext(ctx, "backup.yb_snapshot.schema_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if info.Size() == 0 {
		wrapped := fmt.Errorf("ysql_dump schema produced 0 bytes; refuse to ship empty schema")
		logger.ErrorContext(ctx, "backup.yb_snapshot.schema_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.yb_snapshot.schema_dumped",
		slog.String("path", schemaPath), slog.Int64("bytes", info.Size()))
	return nil
}

// cleanupYBPriorExportSnapshot deletes the newest prior export run's in-cluster
// snapshot once every node prefix its manifest lists is filled. Runs whose
// manifests are still incomplete keep their snapshot, because the lagging
// nodes' archive timers still need the tablet files on disk. Best effort
// throughout: any failure is logged and the new export proceeds.
func cleanupYBPriorExportSnapshot(ctx context.Context, cli *client.Client, cfg *config.Config) {
	logger := telemetry.L(ctx)
	s3Client := newBackupS3Client(cfg)
	runID, err := newestYBSnapshotRunID(ctx, s3Client, cfg.BackupS3BucketMain)
	if err != nil || runID == "" {
		return
	}
	manifest, err := fetchYBSnapshotManifest(ctx, s3Client, cfg.BackupS3BucketMain, runID)
	if err != nil {
		logger.WarnContext(ctx, "backup.yb_snapshot.cleanup_manifest_failed",
			slog.String("run_id", runID), slog.String("err", err.Error()))
		return
	}
	if len(manifest.Nodes) == 0 || manifest.SnapshotID == "" {
		return
	}
	missing, err := missingYBNodeArchives(manifest, func(key string) (bool, error) {
		return objectExists(ctx, s3Client, cfg.BackupS3BucketMain, key)
	})
	if err != nil {
		logger.WarnContext(ctx, "backup.yb_snapshot.cleanup_check_failed",
			slog.String("run_id", runID), slog.String("err", err.Error()))
		return
	}
	if len(missing) > 0 {
		logger.InfoContext(ctx, "backup.yb_snapshot.cleanup_pending",
			slog.String("run_id", runID), slog.Any("missing_nodes", missing))
		return
	}
	if _, delErr := ybAdminOneShot(ctx, cli, cfg, nil, "delete_snapshot", manifest.SnapshotID); delErr != nil {
		logger.WarnContext(ctx, "backup.yb_snapshot.delete_failed",
			slog.String("snapshot_id", manifest.SnapshotID), slog.String("err", delErr.Error()))
		return
	}
	logger.InfoContext(ctx, "backup.yb_snapshot.prior_snapshot_deleted",
		slog.String("run_id", runID), slog.String("snapshot_id", manifest.SnapshotID))
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
